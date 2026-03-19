package telemetry

import "strings"

var namespacePodLabelPairs = [][2]string{
	{"namespace", "pod"},
	{"namespace", "pod_name"},
	{"namespace_name", "pod_name"},
	{"kubernetes_namespace", "pod"},
	{"kubernetes_namespace", "kubernetes_pod_name"},
	{"kubernetes_namespace_name", "pod"},
	{"kubernetes_namespace_name", "kubernetes_pod_name"},
	{"exported_namespace", "pod"},
	{"exported_namespace", "exported_pod"},
}

var namespaceLabelAliases = []string{
	"namespace",
	"namespace_name",
	"kubernetes_namespace",
	"kubernetes_namespace_name",
	"exported_namespace",
}

var podLabelAliases = []string{
	"pod",
	"pod_name",
	"kubernetes_pod_name",
	"exported_pod",
}

func NamespacePodLabelPairs() [][2]string {
	out := make([][2]string, len(namespacePodLabelPairs))
	copy(out, namespacePodLabelPairs)
	return out
}

func NormalizeNamespacePodLabels(metric map[string]string) (string, string) {
	for _, pair := range namespacePodLabelPairs {
		ns := strings.TrimSpace(metric[pair[0]])
		pod := strings.TrimSpace(metric[pair[1]])
		if ns != "" && pod != "" {
			return ns, pod
		}
	}
	return firstNonEmpty(metric, namespaceLabelAliases...), firstNonEmpty(metric, podLabelAliases...)
}

func DetectNamespacePodLabelPair(labels []string) (string, string) {
	set := map[string]struct{}{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		set[label] = struct{}{}
	}
	for _, pair := range namespacePodLabelPairs {
		if _, ok := set[pair[0]]; !ok {
			continue
		}
		if _, ok := set[pair[1]]; !ok {
			continue
		}
		return pair[0], pair[1]
	}
	return "", ""
}

func firstNonEmpty(labels map[string]string, candidates ...string) string {
	for _, candidate := range candidates {
		if value := strings.TrimSpace(labels[candidate]); value != "" {
			return value
		}
	}
	return ""
}
