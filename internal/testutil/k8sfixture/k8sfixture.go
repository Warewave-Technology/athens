// Package k8sfixture loads multi-document Kubernetes YAML fixtures for tests.
package k8sfixture

import (
	"bytes"
	"errors"
	"io"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Set holds the decoded objects of one fixture file.
type Set struct {
	StatefulSets []*appsv1.StatefulSet
	Deployments  []*appsv1.Deployment
	Services     []*corev1.Service
	Secrets      []*corev1.Secret
	ConfigMaps   []*corev1.ConfigMap
	CNPG         []*unstructured.Unstructured
	Zalando      []*unstructured.Unstructured
}

// Typed returns the built-in objects (for a fake clientset).
func (s *Set) Typed() []runtime.Object {
	var out []runtime.Object
	for _, o := range s.StatefulSets {
		out = append(out, o)
	}
	for _, o := range s.Deployments {
		out = append(out, o)
	}
	for _, o := range s.Services {
		out = append(out, o)
	}
	for _, o := range s.Secrets {
		out = append(out, o)
	}
	for _, o := range s.ConfigMaps {
		out = append(out, o)
	}
	return out
}

// Load decodes file, placing every object in namespace. Secret stringData is
// folded into data like the API server does.
func Load(file, namespace string) (*Set, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- test fixtures
	if err != nil {
		return nil, err
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	set := &Set{}
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return set, nil
			}
			return nil, err
		}
		if len(m) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetNamespace(namespace)
		conv := func(into any) error {
			return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into)
		}
		switch u.GetKind() {
		case "StatefulSet":
			o := &appsv1.StatefulSet{}
			if err := conv(o); err != nil {
				return nil, err
			}
			set.StatefulSets = append(set.StatefulSets, o)
		case "Deployment":
			o := &appsv1.Deployment{}
			if err := conv(o); err != nil {
				return nil, err
			}
			set.Deployments = append(set.Deployments, o)
		case "Service":
			o := &corev1.Service{}
			if err := conv(o); err != nil {
				return nil, err
			}
			set.Services = append(set.Services, o)
		case "Secret":
			o := &corev1.Secret{}
			if err := conv(o); err != nil {
				return nil, err
			}
			if o.Data == nil {
				o.Data = map[string][]byte{}
			}
			for k, v := range o.StringData {
				o.Data[k] = []byte(v)
			}
			o.StringData = nil
			set.Secrets = append(set.Secrets, o)
		case "ConfigMap":
			o := &corev1.ConfigMap{}
			if err := conv(o); err != nil {
				return nil, err
			}
			set.ConfigMaps = append(set.ConfigMaps, o)
		case "Cluster":
			set.CNPG = append(set.CNPG, u)
		case "postgresql":
			set.Zalando = append(set.Zalando, u)
		default:
			return nil, errors.New("unsupported fixture kind " + u.GetKind())
		}
	}
}
