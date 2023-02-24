package longhorn

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	snapv1 "github.com/kubernetes-incubator/external-storage/snapshot/pkg/apis/crd/v1"
	snapshotVolume "github.com/kubernetes-incubator/external-storage/snapshot/pkg/volume"
	storkvolume "github.com/libopenstorage/stork/drivers/volume"
	storkapi "github.com/libopenstorage/stork/pkg/apis/stork/v1alpha1"
	"github.com/libopenstorage/stork/pkg/errors"
	"github.com/longhorn/longhorn-manager/client"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/portworx/sched-ops/k8s/storage"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	k8shelper "k8s.io/component-helpers/storage/volume"
)

const (
	// pvcProvisionerAnnotation is the annotation on PVC which has the provisioner name
	pvcProvisionerAnnotation = "volume.kubernetes.io/storage-provisioner"
	// pvProvisionedByAnnotation is the annotation on PV which has the provisioner name
	pvProvisionedByAnnotation = "pv.kubernetes.io/provisioned-by"

	provisionerName = "driver.longhorn.io"

	rackLabelKey = "longhorn/rack"
)

type longhorn struct {
	store       cache.Store
	stopChannel chan struct{}
	storkvolume.ClusterPairNotSupported
	storkvolume.MigrationNotSupported
	storkvolume.GroupSnapshotNotSupported
	storkvolume.ClusterDomainsNotSupported
	storkvolume.BackupRestoreNotSupported
	storkvolume.CloneNotSupported
	storkvolume.SnapshotRestoreNotSupported
}

func (l *longhorn) Init(_ interface{}) error {
	l.stopChannel = make(chan struct{})
	return l.startNodeCache()
}

func (l *longhorn) startNodeCache() error {
	resyncPeriod := 30 * time.Second

	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("error getting cluster config: %v", err)
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("error getting client, %v", err)
	}

	restClient := k8sClient.CoreV1().RESTClient()

	watchlist := cache.NewListWatchFromClient(restClient, "nodes", v1.NamespaceAll, fields.Everything())
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return watchlist.List(options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return watchlist.Watch(options)
		},
	}
	store, controller := cache.NewInformer(lw, &v1.Node{}, resyncPeriod,
		cache.ResourceEventHandlerFuncs{},
	)
	l.store = store

	go controller.Run(l.stopChannel)
	return nil
}

func (l *longhorn) String() string {
	return storkvolume.CSIDriverName
}

func (l *longhorn) InspectNode(id string) (*storkvolume.NodeInfo, error) {
	return nil, &errors.ErrNotSupported{}
}

func contains(a []string, e string) bool {
	for _, x := range a {
		if x == e {
			return true
		}
	}
	return false
}

func (l *longhorn) InspectVolume(volumeID string) (*storkvolume.Info, error) {
	r, err := client.NewRancherClient(&client.ClientOpts{
		Url:     "http://longhorn-frontend.longhorn-system/v1",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	vol, err := r.Volume.ById(volumeID)
	if err != nil {
		return nil, err
	}

	size, err := strconv.ParseUint(vol.Size, 10, 64)
	if err != nil {
		return nil, err

	}

	var nodes []string

	// If this is a RWX volume, only give weight to the share manager's node.
	if smNode := l.getShareManagerNode(vol); smNode != "" {
		logrus.Debugf("Found share-manager for %s on %s", volumeID, smNode)
		nodes = append(nodes, smNode)
	} else {
		for _, r := range vol.Replicas {
			// TODO: check replica state and node selector
			nodes = append(nodes, r.HostId)
		}
	}

	return &storkvolume.Info{
		VolumeID:   vol.Id,
		VolumeName: vol.Name,
		DataNodes:  nodes,
		Size:       size,
	}, nil
}

// shareManager returns the share manager's node if this is a RWX volume.
func (l *longhorn) getShareManagerNode(vol *client.Volume) string {
	if vol == nil {
		return ""
	}

	pods, err := core.Instance().GetPods("longhorn-system", map[string]string{
		"longhorn.io/component": "share-manager",
	})
	if err != nil {
		logrus.Warnf("Error getting share-manager pods: %v", err)
		return ""
	}

	for _, p := range pods.Items {
		managedVol, ok := p.Labels["longhorn.io/share-manager"]
		if !ok {
			continue
		}
		if managedVol != vol.Name {
			continue
		}

		return p.Spec.NodeName
	}

	return ""
}

func (l *longhorn) getNodeLabels(nodeInfo *storkvolume.NodeInfo) (map[string]string, error) {
	obj, exists, err := l.store.GetByKey(nodeInfo.SchedulerID)
	if err != nil {
		return nil, err
	} else if !exists {
		obj, exists, err = l.store.GetByKey(nodeInfo.StorageID)
		if err != nil {
			return nil, err
		} else if !exists {
			obj, exists, err = l.store.GetByKey(nodeInfo.Hostname)
			if err != nil {
				return nil, err
			} else if !exists {
				return nil, fmt.Errorf("node %v/%v/%v not found in cache", nodeInfo.StorageID, nodeInfo.SchedulerID, nodeInfo.Hostname)
			}
		}
	}
	node := obj.(*v1.Node)
	return node.Labels, nil
}

func (l *longhorn) mapLonghornStatus(n client.Node) storkvolume.NodeStatus {
	if n.AllowScheduling {
		return storkvolume.NodeOnline
	}
	return storkvolume.NodeOffline
}

func (l *longhorn) GetNodes() ([]*storkvolume.NodeInfo, error) {
	r, err := client.NewRancherClient(&client.ClientOpts{
		Url:     "http://longhorn-frontend.longhorn-system/v1",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	nodes, err := r.Node.List(&client.ListOpts{})
	if err != nil {
		return nil, fmt.Errorf("failed to get longhorn nodes: %w", err)
	}

	var infos []*storkvolume.NodeInfo
	for _, n := range nodes.Data {
		ips := []string{n.Address}

		newInfo := &storkvolume.NodeInfo{
			StorageID: n.Name,
			Hostname:  n.Name,
			IPs:       ips,
			Status:    l.mapLonghornStatus(n),
		}
		labels, err := l.getNodeLabels(newInfo)
		if err == nil {
			if rack, ok := labels[rackLabelKey]; ok {
				newInfo.Rack = rack
			}
			if zone, ok := labels[v1.LabelZoneFailureDomain]; ok {
				newInfo.Zone = zone
			}
			if region, ok := labels[v1.LabelZoneRegion]; ok {
				newInfo.Region = region
			}
		}
		infos = append(infos, newInfo)
	}
	return infos, nil
}

func (l *longhorn) GetPodVolumes(podSpec *v1.PodSpec, namespace string, includePendingWFFC bool) ([]*storkvolume.Info, []*storkvolume.Info, error) {
	// includePendingWFFC - Includes pending volumes in the second return value if they are using WaitForFirstConsumer binding mode
	var volumes []*storkvolume.Info
	var pendingWFFCVolumes []*storkvolume.Info
	for _, volume := range podSpec.Volumes {
		volumeName := ""
		isPendingWFFC := false
		if volume.PersistentVolumeClaim != nil {
			pvc, err := core.Instance().GetPersistentVolumeClaim(
				volume.PersistentVolumeClaim.ClaimName,
				namespace)
			if err != nil {
				return nil, nil, err
			}

			if !l.OwnsPVC(core.Instance(), pvc) {
				continue
			}

			if pvc.Status.Phase == v1.ClaimPending {
				// Only include pending volume if requested and storage class has WFFC
				if includePendingWFFC && isWaitingForFirstConsumer(pvc) {
					isPendingWFFC = true
				} else {
					return nil, nil, &storkvolume.ErrPVCPending{
						Name: volume.PersistentVolumeClaim.ClaimName,
					}
				}
			}

			volumeName = pvc.Spec.VolumeName
		}

		if volumeName != "" {
			volumeInfo, err := l.InspectVolume(volumeName)
			if err != nil {
				// If the ispect volume fails return with atleast some info
				volumeInfo = &storkvolume.Info{
					VolumeName: volumeName,
				}
			}
			if isPendingWFFC {
				pendingWFFCVolumes = append(pendingWFFCVolumes, volumeInfo)
			} else {
				volumes = append(volumes, volumeInfo)
			}
		}
	}

	// If this is an RWX NFS share manager pod, include its volume too. Hacky
	// because we only have the spec, not labels to tell us which PVC it
	// serves.
	for _, c := range podSpec.Containers {
		if c.Name != "share-manager" {
			continue
		}
		for _, a := range c.Args {
			if !strings.HasPrefix(a, "pvc-") {
				continue
			}
			volumeInfo, err := l.InspectVolume(a)
			if err != nil {
				break
			}
			volumes = append(volumes, volumeInfo)

		}
	}

	return volumes, pendingWFFCVolumes, nil
}

func isWaitingForFirstConsumer(pvc *v1.PersistentVolumeClaim) bool {
	var sc *storagev1.StorageClass
	var err error
	storageClassName := k8shelper.GetPersistentVolumeClaimClass(pvc)
	if storageClassName != "" {
		sc, err = storage.Instance().GetStorageClass(storageClassName)
		if err != nil {
			logrus.Warnf("Did not get the storageclass %s for pvc %s/%s, err: %v", storageClassName, pvc.Namespace, pvc.Name, err)
			return false
		}
		return *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer
	}
	return false
}

func (l *longhorn) GetVolumeClaimTemplates(templates []v1.PersistentVolumeClaim) ([]v1.PersistentVolumeClaim, error) {
	var longhornTemplates []v1.PersistentVolumeClaim
	for _, t := range templates {
		if l.OwnsPVC(core.Instance(), &t) {
			longhornTemplates = append(longhornTemplates, t)
		}
	}
	return longhornTemplates, nil
}

func (l *longhorn) OwnsPVCForBackup(coreOps core.Ops, pvc *v1.PersistentVolumeClaim, cmBackupType string, crBackupType string) bool {
	return l.OwnsPVC(coreOps, pvc)
}

func (l *longhorn) OwnsPVC(coreOps core.Ops, pvc *v1.PersistentVolumeClaim) bool {
	provisioner := ""
	// Check for the provisioner in the PVC annotation. If not populated
	// try getting the provisioner from the Storage class.
	if val, ok := pvc.Annotations[pvcProvisionerAnnotation]; ok {
		provisioner = val
	} else {
		storageClassName := k8shelper.GetPersistentVolumeClaimClass(pvc)
		if storageClassName != "" {
			storageClass, err := storage.Instance().GetStorageClass(storageClassName)
			if err == nil {
				provisioner = storageClass.Provisioner
			} else {
				logrus.Warnf("Error getting storageclass %v for pvc %v: %v", storageClassName, pvc.Name, err)
			}
		}
	}

	if provisioner == "" {
		// Try to get info from the PV since storage class could be deleted
		pv, err := coreOps.GetPersistentVolume(pvc.Spec.VolumeName)
		if err != nil {
			logrus.Warnf("Error getting pv %v for pvc %v: %v", pvc.Spec.VolumeName, pvc.Name, err)
			return false
		}
		return l.OwnsPV(pv)
	}

	if provisioner == provisionerName {
		return true
	}

	return false
}

func (l *longhorn) OwnsPV(pv *v1.PersistentVolume) bool {
	provisioner := ""
	// Check the annotation in the PV for the provisioner
	if val, ok := pv.Annotations[pvProvisionedByAnnotation]; ok {
		provisioner = val
	}
	if provisioner == provisionerName {
		return true
	}
	return false
}

func (l *longhorn) GetSnapshotPlugin() snapshotVolume.Plugin {
	return nil
}

func (l *longhorn) GetSnapshotType(snap *snapv1.VolumeSnapshot) (string, error) {
	return "", &errors.ErrNotImplemented{}
}

func (l *longhorn) Stop() error {
	close(l.stopChannel)
	return nil
}

func (l *longhorn) UpdateMigratedPersistentVolumeSpec(
	pv *v1.PersistentVolume,
	vInfo *storkapi.ApplicationRestoreVolumeInfo,
) (*v1.PersistentVolume, error) {

	return pv, nil

}

func (l *longhorn) GetClusterID() (string, error) {
	return "longhorn", nil
}

func init() {
	l := &longhorn{}
	if err := storkvolume.Register(storkvolume.CSIDriverName, l); err != nil {
		logrus.Panicf("Error registering longhorn volume driver: %v", err)
	}
}
