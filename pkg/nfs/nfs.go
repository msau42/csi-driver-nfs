/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nfs

import (
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"k8s.io/utils/mount"
)

type nfsDriver struct {
	name    string
	nodeID  string
	version string

	endpoint string

	//ids *identityServer
	ns    *nodeServer
	cap   map[csi.VolumeCapability_AccessMode_Mode]bool
	cscap []*csi.ControllerServiceCapability
}

const (
	driverName       = "nfs.csi.k8s.io"
	internalMountDir = "/provisioner-working-mounts"
)

var (
	version = "2.0.0"
)

func NewNFSdriver(nodeID, endpoint string) *nfsDriver {
	glog.Infof("Driver: %v version: %v", driverName, version)

	n := &nfsDriver{
		name:     driverName,
		version:  version,
		nodeID:   nodeID,
		endpoint: endpoint,
		cap:      map[csi.VolumeCapability_AccessMode_Mode]bool{},
	}

	vcam := []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	}
	n.AddVolumeCapabilityAccessModes(vcam)

	n.AddControllerServiceCapabilities([]csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME})
	return n
}

func NewNodeServer(n *nfsDriver, mounter mount.Interface) *nodeServer {
	return &nodeServer{
		Driver:  n,
		mounter: mounter,
	}
}

func (n *nfsDriver) Run() {
	n.ns = NewNodeServer(n, mount.New(""))
	s := NewNonBlockingGRPCServer()
	s.Start(n.endpoint,
		NewDefaultIdentityServer(n),
		// NFS plugin has not implemented ControllerServer
		// using default controllerserver.
		NewControllerServer(n, internalMountDir),
		n.ns)
	s.Wait()
}

func (n *nfsDriver) AddVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) []*csi.VolumeCapability_AccessMode {
	var vca []*csi.VolumeCapability_AccessMode
	for _, c := range vc {
		glog.Infof("Enabling volume access mode: %v", c.String())
		vca = append(vca, &csi.VolumeCapability_AccessMode{Mode: c})
		n.cap[c] = true
	}
	return vca
}

func (n *nfsDriver) AddControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) {
	var csc []*csi.ControllerServiceCapability

	for _, c := range cl {
		glog.Infof("Enabling controller service capability: %v", c.String())
		csc = append(csc, NewControllerServiceCapability(c))
	}

	n.cscap = csc

	return
}
