package nfs

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"k8s.io/utils/mount"
)

const (
	testServer    = "test-server"
	testBaseDir   = "test-base-dir"
	testCSIVolume = "test-csi"
	testVolumeId  = "test-server/test-base-dir/test-csi"
	testShare     = "/test-base-dir/test-csi"
)

func initTestController(t *testing.T) *ControllerServer {
	tmpDir, err := ioutil.TempDir(os.TempDir(), "csi-nfs-controller-test")
	if err != nil {
		t.Fatalf("failed to create tmp testing dir")
	}
	defer os.RemoveAll(tmpDir)

	mounter := &mount.FakeMounter{MountPoints: []mount.MountPoint{}}
	driver := NewNFSdriver("", "")
	driver.ns = NewNodeServer(driver, mounter)
	return NewControllerServer(driver, tmpDir)
}

func TestCreateVolume(t *testing.T) {
	cases := []struct {
		name      string
		req       *csi.CreateVolumeRequest
		resp      *csi.CreateVolumeResponse
		expectErr bool
	}{
		{
			name: "valid defaults",
			req: &csi.CreateVolumeRequest{
				Name: testCSIVolume,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					paramServer:  testServer,
					paramBaseDir: testBaseDir,
				},
			},
			resp: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					VolumeId: testVolumeId,
					VolumeContext: map[string]string{
						attrServer: testServer,
						attrShare:  testShare,
					},
				},
			},
		},
		{
			name: "name empty",
			req: &csi.CreateVolumeRequest{
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					paramServer:  testServer,
					paramBaseDir: testBaseDir,
				},
			},
			expectErr: true,
		},
		{
			name: "invalid volume capability",
			req: &csi.CreateVolumeRequest{
				Name: testCSIVolume,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					paramServer:  testServer,
					paramBaseDir: testBaseDir,
				},
			},
			expectErr: true,
		},
		{
			name: "invalid create context",
			req: &csi.CreateVolumeRequest{
				Name: testCSIVolume,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"unknown-parameter": "foo",
				},
			},
			expectErr: true,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Setup
			cs := initTestController(t)

			// Run
			resp, err := cs.CreateVolume(context.TODO(), test.req)

			// Verify
			if !test.expectErr && err != nil {
				t.Errorf("test %q failed: %v", test.name, err)
			}
			if test.expectErr && err == nil {
				t.Errorf("test %q failed; got success", test.name)
			}
			if !reflect.DeepEqual(resp, test.resp) {
				t.Errorf("test %q failed: got resp %+v, expected %+v", test.name, resp, test.resp)
			}
			if !test.expectErr {
				info, err := os.Stat(filepath.Join(cs.workingMountDir, test.req.Name, test.req.Name))
				if err != nil {
					t.Errorf("test %q failed: couldn't find volume subdirectory: %v", test.name, err)
				}
				if !info.IsDir() {
					t.Errorf("test %q failed: subfile not a directory", test.name)
				}
			}
		})
	}
}

func TestDeleteVolume(t *testing.T) {
	cases := []struct {
		name             string
		req              *csi.DeleteVolumeRequest
		internalMountDir string
		expectErr        bool
	}{
		{
			name: "valid",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeId,
			},
			internalMountDir: filepath.Join(testCSIVolume, testCSIVolume),
		},
		{
			name: "invalid id",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeId + "/foo",
			},
		},
		{
			name:      "empty id",
			req:       &csi.DeleteVolumeRequest{},
			expectErr: true,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Setup
			var internalMountPath string
			cs := initTestController(t)
			if test.internalMountDir != "" {
				internalMountPath = filepath.Join(cs.workingMountDir, test.internalMountDir)
				if err := os.MkdirAll(internalMountPath, 0755); err != nil {
					t.Fatalf("test %q failed: failed to setup volume: %v", test.name, err)
				}
			}

			// Run
			_, err := cs.DeleteVolume(context.TODO(), test.req)

			// Verify
			if !test.expectErr && err != nil {
				t.Errorf("test %q failed: %v", test.name, err)
			}
			if test.expectErr && err == nil {
				t.Errorf("test %q failed; got success", test.name)
			}
			if !test.expectErr {
				_, err := os.Stat(internalMountPath)
				if err != nil && !os.IsNotExist(err) {
					t.Errorf("test %q failed: couldn't get info on subdirectory: %v", test.name, err)
				} else if err == nil {
					t.Errorf("test %q failed: subdirectory still exists: %v", test.name, err)
				}
			}
		})
	}
}

func TestGenerateNewNFSVolume(t *testing.T) {
	cases := []struct {
		name      string
		params    map[string]string
		expectVol *nfsVolume
		expectErr bool
	}{
		{
			name: "required params",
			params: map[string]string{
				paramServer:  testServer,
				paramBaseDir: testBaseDir,
			},
			expectVol: &nfsVolume{
				id:      testVolumeId,
				server:  testServer,
				baseDir: testBaseDir,
				subDir:  testCSIVolume,
			},
		},
		{
			name: "missing required baseDir",
			params: map[string]string{
				paramServer: testServer,
			},
			expectErr: true,
		},
		{
			name: "missing required server",
			params: map[string]string{
				paramBaseDir: testBaseDir,
			},
			expectErr: true,
		},
		{
			name: "invalid params",
			params: map[string]string{
				"foo-param": "bar",
			},
			expectErr: true,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cs := initTestController(t)
			vol, err := cs.newNFSVolume(testCSIVolume, test.params)
			if !test.expectErr && err != nil {
				t.Errorf("test %q failed: %v", test.name, err)
			}
			if test.expectErr && err == nil {
				t.Errorf("test %q failed; got success", test.name)
			}
			if !reflect.DeepEqual(vol, test.expectVol) {
				t.Errorf("test %q failed: got volume %+v, expected %+v", test.name, vol, test.expectVol)
			}
		})
	}
}

func TestGetNfsVolFromId(t *testing.T) {
	cases := []struct {
		name      string
		id        string
		expectVol *nfsVolume
		expectErr bool
	}{
		{
			name: "valid id",
			id:   testVolumeId,
			expectVol: &nfsVolume{
				id:      testVolumeId,
				server:  testServer,
				baseDir: testBaseDir,
				subDir:  testCSIVolume,
			},
		},
		{
			name:      "empty id",
			id:        "",
			expectErr: true,
		},
		{
			name:      "not enough elements",
			id:        strings.Join([]string{testServer, testBaseDir}, "/"),
			expectErr: true,
		},
		{
			name:      "too many elements",
			id:        strings.Join([]string{testServer, testBaseDir, testCSIVolume, "more"}, "/"),
			expectErr: true,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cs := initTestController(t)
			vol, err := cs.getNfsVolFromId(test.id)
			if !test.expectErr && err != nil {
				t.Errorf("test %q failed: %v", test.name, err)
			}
			if test.expectErr && err == nil {
				t.Errorf("test %q failed; got success", test.name)
			}
			if !reflect.DeepEqual(vol, test.expectVol) {
				t.Errorf("test %q failed: got volume %+v, expected %+v", test.name, vol, test.expectVol)
			}
		})
	}
}
