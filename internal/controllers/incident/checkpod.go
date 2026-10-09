package incident

import (
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/krateo-platformops/incident-controller/apis/incident/v1alpha1"
)

// Labels and annotations on check pods and their ConfigMaps.
const (
	LabelComponent = "app.kubernetes.io/component"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ComponentCheck marks check pods; the chart's NetworkPolicy selects it.
	ComponentCheck = "incident-check"
	managedBy      = "incident-controller"

	labelIncidentUID = "observability.krateo.io/incident-uid"
	labelScript      = "observability.krateo.io/check-script"

	annotationIncidentNamespace = "observability.krateo.io/incident-namespace"
	annotationIncidentName      = "observability.krateo.io/incident-name"

	// Apply pods carry no incident UID label and no incident annotations: the Incident controller
	// never sees them as its own check pods.
	labelApplyUID            = "observability.krateo.io/incidentapply-uid"
	annotationApplyNamespace = "observability.krateo.io/incidentapply-namespace"
	annotationApplyName      = "observability.krateo.io/incidentapply-name"
)

const (
	// noNameserver is the check pods' only nameserver; nothing listens there.
	noNameserver = "127.0.0.1"

	containerName    = "check"
	scriptVolume     = "script"
	tmpVolume        = "tmp"
	kubeconfigVolume = "kubeconfig"
	scriptDir        = "/check"
	scriptKey        = "check.sh"
	kubeconfigDir    = "/kube"
	kubeconfigKey    = "config"
)

// CheckPods builds the pods that run precondition and verify.
type CheckPods struct {
	// Namespace the check pods run in.
	Namespace string
	// Template is the chart-rendered pod spec: service account, image, resources, scheduling.
	Template corev1.PodSpec
	// Timeout is the pod's activeDeadlineSeconds.
	Timeout time.Duration
}

// LoadPodTemplate reads the check pod template: a pod spec with one container and a service
// account.
func LoadPodTemplate(path string) (corev1.PodSpec, error) {
	var spec corev1.PodSpec
	b, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}
	if err := yaml.UnmarshalStrict(b, &spec); err != nil {
		return spec, fmt.Errorf("check pod template %s: %w", path, err)
	}
	if len(spec.Containers) != 1 {
		return spec, fmt.Errorf("check pod template %s: want exactly one container, got %d", path, len(spec.Containers))
	}
	if spec.ServiceAccountName == "" {
		return spec, fmt.Errorf("check pod template %s: serviceAccountName is required", path)
	}
	for _, v := range spec.Volumes {
		if v.Name == scriptVolume || v.Name == tmpVolume || v.Name == kubeconfigVolume {
			return spec, fmt.Errorf("check pod template %s: volume name %q is reserved", path, v.Name)
		}
	}
	return spec, nil
}

// selector matches the check pods and ConfigMaps of one incident.
func selector(inc *v1alpha1.Incident) map[string]string {
	return map[string]string{labelIncidentUID: string(inc.UID)}
}

// checkName names the pod and ConfigMap of the run of a script that became due a poll interval
// after from (see checkFrom). A create retried after a partial failure reuses the same name.
func checkName(inc *v1alpha1.Incident, s v1alpha1.Script, from time.Time) string {
	uid := strings.ReplaceAll(string(inc.UID), "-", "")
	if len(uid) > 12 {
		uid = uid[:12]
	}
	var seq int64
	if !from.IsZero() {
		seq = from.Unix()
	}
	return fmt.Sprintf("check-%s-%s-%d", uid, s, seq)
}

func (p CheckPods) meta(inc *v1alpha1.Incident, name string, s v1alpha1.Script) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: p.Namespace,
		Labels: map[string]string{
			LabelComponent:   ComponentCheck,
			LabelManagedBy:   managedBy,
			labelIncidentUID: string(inc.UID),
			labelScript:      string(s),
		},
		Annotations: map[string]string{
			annotationIncidentNamespace: inc.Namespace,
			annotationIncidentName:      inc.Name,
		},
	}
}

// Pod is the check pod for the run of a script that became due a poll interval after from.
func (p CheckPods) Pod(inc *v1alpha1.Incident, s v1alpha1.Script, from time.Time) *corev1.Pod {
	pod := p.sandbox(p.meta(inc, checkName(inc, s, from), s), p.Timeout)
	pod.Spec.AutomountServiceAccountToken = ptr.To(true)
	return pod
}

// sandbox is a pod that runs the script in the ConfigMap named after it. The template supplies
// identity, image and scheduling; the sandbox settings are enforced here whatever the template
// says.
func (p CheckPods) sandbox(m metav1.ObjectMeta, timeout time.Duration) *corev1.Pod {
	spec := *p.Template.DeepCopy()

	spec.RestartPolicy = corev1.RestartPolicyNever
	spec.ActiveDeadlineSeconds = ptr.To(int64(timeout / time.Second))
	spec.EnableServiceLinks = ptr.To(false)
	spec.HostNetwork, spec.HostPID, spec.HostIPC = false, false, false
	// No name resolution: a lookup reaching cluster DNS is forwarded outside and carries data out.
	// kubectl reaches the apiserver by IP.
	spec.DNSPolicy = corev1.DNSNone
	spec.DNSConfig = &corev1.PodDNSConfig{Nameservers: []string{noNameserver}}
	if spec.SecurityContext == nil {
		spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	spec.SecurityContext.RunAsNonRoot = ptr.To(true)
	if spec.SecurityContext.SeccompProfile == nil {
		spec.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	}
	spec.Volumes = append(spec.Volumes,
		corev1.Volume{Name: scriptVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: m.Name},
			DefaultMode:          ptr.To(int32(0o444)),
		}}},
		corev1.Volume{Name: tmpVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			SizeLimit: ptr.To(resource.MustParse("16Mi")),
		}}},
	)

	c := &spec.Containers[0]
	c.Name = containerName
	c.Command = []string{"bash", scriptDir + "/" + scriptKey}
	c.Args = nil
	c.Env = append(c.Env, corev1.EnvVar{Name: "HOME", Value: "/tmp"})
	c.VolumeMounts = append(c.VolumeMounts,
		corev1.VolumeMount{Name: scriptVolume, MountPath: scriptDir, ReadOnly: true},
		corev1.VolumeMount{Name: tmpVolume, MountPath: "/tmp"},
	)
	if c.SecurityContext == nil {
		c.SecurityContext = &corev1.SecurityContext{}
	}
	c.SecurityContext.Privileged = ptr.To(false)
	c.SecurityContext.AllowPrivilegeEscalation = ptr.To(false)
	c.SecurityContext.ReadOnlyRootFilesystem = ptr.To(true)
	c.SecurityContext.RunAsNonRoot = ptr.To(true)
	c.SecurityContext.Capabilities = &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	c.TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError

	return &corev1.Pod{ObjectMeta: m, Spec: spec}
}

// ConfigMap holds the script a check pod runs. It is owned by the pod, so it goes with it.
func (p CheckPods) ConfigMap(inc *v1alpha1.Incident, pod *corev1.Pod, s v1alpha1.Script) *corev1.ConfigMap {
	m := p.meta(inc, pod.Name, s)
	m.OwnerReferences = ownedBy(pod)
	return &corev1.ConfigMap{ObjectMeta: m, Data: map[string]string{scriptKey: scriptBody(inc, s)}}
}

// applyName names the pod, kubeconfig Secret and ConfigMap of an IncidentApply's run. One run per
// IncidentApply, so the name depends only on its UID.
func applyName(ia *v1alpha1.IncidentApply) string {
	return "apply-" + strings.ReplaceAll(string(ia.UID), "-", "")
}

func (p CheckPods) applyMeta(ia *v1alpha1.IncidentApply) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      applyName(ia),
		Namespace: p.Namespace,
		Labels: map[string]string{
			LabelComponent: ComponentCheck,
			LabelManagedBy: managedBy,
			labelApplyUID:  string(ia.UID),
			labelScript:    string(v1alpha1.ScriptApply),
		},
		Annotations: map[string]string{
			annotationApplyNamespace: ia.Namespace,
			annotationApplyName:      ia.Name,
		},
	}
}

// ApplyPod is the pod that runs an IncidentApply's script. It is a check pod with no service
// account token: kubectl authenticates with the requester's kubeconfig, mounted from the Secret
// named after the pod.
func (p CheckPods) ApplyPod(ia *v1alpha1.IncidentApply, timeout time.Duration) *corev1.Pod {
	pod := p.sandbox(p.applyMeta(ia), timeout)
	pod.Spec.AutomountServiceAccountToken = ptr.To(false)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: kubeconfigVolume, VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{SecretName: pod.Name, DefaultMode: ptr.To(int32(0o444))},
	}})
	c := &pod.Spec.Containers[0]
	c.Env = append(c.Env, corev1.EnvVar{Name: "KUBECONFIG", Value: kubeconfigDir + "/" + kubeconfigKey})
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: kubeconfigVolume, MountPath: kubeconfigDir, ReadOnly: true})
	return pod
}

// ApplySecret holds the requester's kubeconfig for an apply pod. It is owned by the pod, so the
// credential goes with it.
func (p CheckPods) ApplySecret(ia *v1alpha1.IncidentApply, pod *corev1.Pod, kubeconfig []byte) *corev1.Secret {
	m := p.applyMeta(ia)
	m.OwnerReferences = ownedBy(pod)
	return &corev1.Secret{ObjectMeta: m, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{kubeconfigKey: kubeconfig}}
}

// ApplyConfigMap holds the script an apply pod runs. It is owned by the pod.
func (p CheckPods) ApplyConfigMap(ia *v1alpha1.IncidentApply, pod *corev1.Pod, script string) *corev1.ConfigMap {
	m := p.applyMeta(ia)
	m.OwnerReferences = ownedBy(pod)
	return &corev1.ConfigMap{ObjectMeta: m, Data: map[string]string{scriptKey: script}}
}

// ownedBy makes the pod the controller owner, so the object is deleted with it.
func ownedBy(pod *corev1.Pod) []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		UID:        pod.UID,
		Controller: ptr.To(true),
	}}
}

// resultOf reads a check pod. done is false while the pod may still produce a result. A pod
// killed by activeDeadlineSeconds, one that never ran, and one past its deadline without
// finishing give a result with no exit code. Result times are deterministic.
func resultOf(pod *corev1.Pod, timeout time.Duration, now time.Time) (r result, done bool) {
	r.script = v1alpha1.Script(pod.Labels[labelScript])
	overdue := pod.CreationTimestamp.Add(timeout + pendingGrace)

	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		if now.Before(overdue) {
			return r, false
		}
		r.at = overdue
		return r, true
	}

	r.at = overdue
	if pod.Status.StartTime != nil {
		r.at = pod.Status.StartTime.Add(timeout)
	}
	for _, cs := range pod.Status.ContainerStatuses {
		t := cs.State.Terminated
		if cs.Name != containerName || t == nil {
			continue
		}
		if !t.FinishedAt.IsZero() {
			r.at = t.FinishedAt.Time
		}
		if pod.Status.Reason != "DeadlineExceeded" && t.ExitCode >= 0 && t.ExitCode <= 255 {
			r.exit = ptr.To(t.ExitCode)
		}
	}
	return r, true
}
