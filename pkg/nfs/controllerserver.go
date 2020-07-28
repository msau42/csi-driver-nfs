package nfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ControllerServer struct {
	Driver *nfsDriver
	// Working directory for the provisioner to temporarily mount nfs shares at
	workingMountDir string
}

// nfsVolume is an internal representation of a volume
// created by the provisioner.
type nfsVolume struct {
	// Volume id
	id string
	// Address of the NFS server.
	// Matches paramServer.
	server string
	// Base directory of the NFS server to create volumes under
	// Matches paramBaseDir.
	baseDir string
	// Subdirectory of the NFS server to create volumes under
	subDir string
}

// CSI CreateVolume parameters
const (
	// Address of the NFS server
	paramServer = "server"
	// Base directory of the NFS server to create volumes under.
	// The base directory must be a direct child of the root directory.
	// The root directory is ommitted from the string, for example:
	//     "base" instead of "/base"
	paramBaseDir = "base-dir"
)

// CSI Volume attributes
const (
	// Address of the NFS server
	attrServer = "server"
	// Path to the NFS share on the server
	attrShare = "share"
)

// Ordering of elements in the CSI volume id.
// ID is of the form {server}/{baseDir}/{subDir}.
// TODO: This volume id format limits baseDir and
// subDir to only be one directory deep.
// Adding a new element should always go at the end
// before totalIDElements
const (
	idServer = iota
	idBaseDir
	idSubDir
	totalIDElements // Always last
)

func NewControllerServer(d *nfsDriver, workingDir string) *ControllerServer {
	return &ControllerServer{
		Driver:          d,
		workingMountDir: workingDir,
	}
}

func (cs *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	glog.V(4).Infof("CreateVolume called with request %+v", req)

	// Validate arguments
	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume name must be provided")
	}

	if err := cs.validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	nfsVol, err := cs.newNFSVolume(name, req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Mount nfs base share so we can create a subdirectory
	if err = cs.internalMount(ctx, nfsVol); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount nfs server: %v", err.Error())
	}
	defer func() {
		if err = cs.internalUnmount(ctx, nfsVol); err != nil {
			glog.Warningf("failed to unmount nfs server: %v", err.Error())
		}
	}()

	// Create subdirectory under base-dir
	// TODO: revisit permissions
	internalVolumePath := cs.getInternalVolumePath(nfsVol)
	glog.V(4).Infof("Creating subdirectory at %v", internalVolumePath)
	if err = os.Mkdir(internalVolumePath, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to make subdirectory: %v", err.Error())
	}
	// Remove capacity setting when provisioner 1.4.0 is available with fix for
	// https://github.com/kubernetes-csi/external-provisioner/pull/271
	return &csi.CreateVolumeResponse{Volume: cs.nfsVolToCSI(nfsVol, req.GetCapacityRange().GetRequiredBytes())}, nil
}

func (cs *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	glog.V(4).Infof("DeleteVolume called with request %+v", req)

	volumeId := req.GetVolumeId()
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is empty")
	}
	nfsVol, err := cs.getNfsVolFromId(volumeId)
	if err != nil {
		// An invalid ID should be treated as doesn't exist
		glog.V(5).Infof("failed to get nfs volume for volume id %v deletion: %v", volumeId, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Mount nfs base share so we can delete the subdirectory
	if err = cs.internalMount(ctx, nfsVol); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount nfs server: %v", err.Error())
	}
	defer func() {
		if err = cs.internalUnmount(ctx, nfsVol); err != nil {
			glog.Warningf("failed to unmount nfs server: %v", err.Error())
		}
	}()

	// Delete subdirectory under base-dir
	internalVolumePath := cs.getInternalVolumePath(nfsVol)
	glog.V(4).Infof("Removing subdirectory at %v", internalVolumePath)
	if err = os.RemoveAll(internalVolumePath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete subdirectory: %v", err.Error())
	}

	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// ControllerGetCapabilities implements the default GRPC callout.
// Default supports all capabilities
func (cs *ControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: cs.Driver.cscap,
	}, nil
}

func (cs *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) validateVolumeCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return fmt.Errorf("volume capabilities must be provided")
	}

	for _, c := range caps {
		if err := cs.validateVolumeCapability(c); err != nil {
			return err
		}
	}
	return nil
}

func (cs *ControllerServer) validateVolumeCapability(c *csi.VolumeCapability) error {
	if c == nil {
		return fmt.Errorf("volume capability must be provided")
	}

	// Validate access mode
	accessMode := c.GetAccessMode()
	if accessMode == nil {
		return fmt.Errorf("volume capability access mode not set")
	}
	if !cs.Driver.cap[accessMode.Mode] {
		return fmt.Errorf("driver does not support access mode: %v", accessMode.Mode.String())
	}

	// Validate access type
	accessType := c.GetAccessType()
	if accessType == nil {
		return fmt.Errorf("volume capability access type not set")
	}
	mountType := c.GetMount()
	if mountType == nil {
		return fmt.Errorf("driver only supports mount access type volume capability")
	}

	if mountType.FsType != "" {
		// TODO: uncomment once https://github.com/kubernetes-csi/external-provisioner/issues/328
		// is fixed
		// return fmt.Errorf("driver does not support fstype %v", mountType.FsType)
	}
	// TODO: check if we want to whitelist/blacklist certain mount options
	return nil
}

// Mount nfs server at base-dir
func (cs *ControllerServer) internalMount(ctx context.Context, vol *nfsVolume) error {
	sharePath := filepath.Join("/" + vol.baseDir)
	targetPath := cs.getInternalMountPath(vol)

	glog.V(4).Infof("internally mounting %v:%v at %v", vol.server, sharePath, targetPath)
	_, err := cs.Driver.ns.NodePublishVolume(ctx, &csi.NodePublishVolumeRequest{
		TargetPath: targetPath,
		VolumeContext: map[string]string{
			"server": vol.server,
			"share":  sharePath,
		},
	})
	return err
}

// Unmount nfs server at base-dir
func (cs *ControllerServer) internalUnmount(ctx context.Context, vol *nfsVolume) error {
	targetPath := cs.getInternalMountPath(vol)

	// Unmount nfs server at base-dir
	glog.V(4).Infof("internally unmounting %v", targetPath)
	_, err := cs.Driver.ns.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{
		TargetPath: cs.getInternalMountPath(vol),
	})
	return err
}

// Convert VolumeCreate parameters to an nfsVolume
func (cs *ControllerServer) newNFSVolume(name string, params map[string]string) (*nfsVolume, error) {
	var (
		server  string
		baseDir string
	)

	// Validate parameters (case-insensitive).
	// TODO do more strict validation.
	for k, v := range params {
		switch strings.ToLower(k) {
		case paramServer:
			server = v
		case paramBaseDir:
			baseDir = v
		default:
			return nil, fmt.Errorf("invalid parameter %q", k)
		}
	}

	// Validate required parameters
	if server == "" {
		return nil, fmt.Errorf("%v is a required parameter", paramServer)
	}
	if baseDir == "" {
		return nil, fmt.Errorf("%v is a required parameter", paramBaseDir)
	}

	vol := &nfsVolume{
		server:  server,
		baseDir: baseDir,
		subDir:  name,
	}
	vol.id = cs.getVolumeIdFromNfsVol(vol)

	return vol, nil
}

// Get working directory for CreateVolume and DeleteVolume
func (cs *ControllerServer) getInternalMountPath(vol *nfsVolume) string {
	return filepath.Join(cs.workingMountDir, vol.subDir)
}

// Get internal path where the volume is created
// The reason why the internal path is "workingDir/subDir/subDir" is because:
//   * the semantic is actually "workingDir/volId/subDir" and volId == subDir.
//   * we need a mount directory per volId because you can have multiple
//     CreateVolume calls in parallel and they may use the same underlying share.
//     Instead of refcounting how many CreateVolume calls are using the same
//     share, it's simpler to just do a mount per request.
func (cs *ControllerServer) getInternalVolumePath(vol *nfsVolume) string {
	return filepath.Join(cs.getInternalMountPath(vol), vol.subDir)
}

// Get user-visible share path for the volume
func (cs *ControllerServer) getVolumeSharePath(vol *nfsVolume) string {
	return filepath.Join("/", vol.baseDir, vol.subDir)
}

// Convert into nfsVolume into a csi.Volume
func (cs *ControllerServer) nfsVolToCSI(vol *nfsVolume, reqBytes int64) *csi.Volume {
	return &csi.Volume{
		CapacityBytes: reqBytes,
		VolumeId:      vol.id,
		VolumeContext: map[string]string{
			attrServer: vol.server,
			attrShare:  cs.getVolumeSharePath(vol),
		},
	}
}

// Given a CSI volume id, return a nfsVolume
func (cs *ControllerServer) getNfsVolFromId(id string) (*nfsVolume, error) {
	tokens := strings.Split(id, "/")
	if len(tokens) != totalIDElements {
		return nil, fmt.Errorf("volume id %q unexpected format: got %v tokens", id, len(tokens))
	}

	return &nfsVolume{
		id:      id,
		server:  tokens[idServer],
		baseDir: tokens[idBaseDir],
		subDir:  tokens[idSubDir],
	}, nil
}

// Given a nfsVolume, return a CSI volume id
func (cs *ControllerServer) getVolumeIdFromNfsVol(vol *nfsVolume) string {
	idElements := make([]string, totalIDElements)
	idElements[idServer] = vol.server
	idElements[idBaseDir] = vol.baseDir
	idElements[idSubDir] = vol.subDir
	return strings.Join(idElements, "/")
}
