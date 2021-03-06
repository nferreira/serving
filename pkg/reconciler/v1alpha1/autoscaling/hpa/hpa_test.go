/*
Copyright 2018 The Knative Authors

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

package hpa

import (
	"context"
	"testing"

	"github.com/knative/pkg/controller"
	autoscalingv1alpha1 "github.com/knative/serving/pkg/apis/autoscaling/v1alpha1"
	"github.com/knative/serving/pkg/apis/networking"
	nv1a1 "github.com/knative/serving/pkg/apis/networking/v1alpha1"
	fakeKna "github.com/knative/serving/pkg/client/clientset/versioned/fake"
	informers "github.com/knative/serving/pkg/client/informers/externalversions"
	"github.com/knative/serving/pkg/reconciler"
	"github.com/knative/serving/pkg/reconciler/v1alpha1/autoscaling/hpa/resources"
	aresources "github.com/knative/serving/pkg/reconciler/v1alpha1/autoscaling/resources"
	. "github.com/knative/serving/pkg/reconciler/v1alpha1/testing"
	presources "github.com/knative/serving/pkg/resources"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeinformers "k8s.io/client-go/informers"
	fakeK8s "k8s.io/client-go/kubernetes/fake"
	fakescaleclient "k8s.io/client-go/scale/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	testNamespace = "test-namespace"
	testRevision  = "test-revision"
)

func TestControllerCanReconcile(t *testing.T) {
	kubeClient := fakeK8s.NewSimpleClientset()
	servingClient := fakeKna.NewSimpleClientset()

	scaleClient := &fakescaleclient.FakeScaleClient{kubeClient.Fake}
	scaleClient.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, scaleResource(testNamespace, testRevision, withLabelSelector("a=b")), nil
	})
	opts := reconciler.Options{
		KubeClientSet:    kubeClient,
		ServingClientSet: servingClient,
		ScaleClientSet:   scaleClient,
		Logger:           TestLogger(t),
	}

	servingInformer := informers.NewSharedInformerFactory(servingClient, 0)
	kubeInformer := kubeinformers.NewSharedInformerFactory(kubeClient, 0)

	ctl := NewController(&opts,
		servingInformer.Autoscaling().V1alpha1().PodAutoscalers(),
		servingInformer.Networking().V1alpha1().ServerlessServices(),
		kubeInformer.Autoscaling().V1().HorizontalPodAutoscalers(),
	)

	podAutoscaler := pa(testRevision, testNamespace, WithHPAClass)
	servingClient.AutoscalingV1alpha1().PodAutoscalers(testNamespace).Create(podAutoscaler)
	servingInformer.Autoscaling().V1alpha1().PodAutoscalers().Informer().GetIndexer().Add(podAutoscaler)

	err := ctl.Reconciler.Reconcile(context.Background(), testNamespace+"/"+testRevision)
	if err != nil {
		t.Errorf("Reconcile() = %v", err)
	}

	_, err = kubeClient.AutoscalingV1().HorizontalPodAutoscalers(testNamespace).Get(testRevision, metav1.GetOptions{})
	if err != nil {
		t.Errorf("error getting hpa: %v", err)
	}
}

func TestReconcile(t *testing.T) {
	var usualSelector = map[string]string{"a": "b"}
	table := TableTest{{
		Name: "create hpa",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
		},
		Key: key(testRevision, testNamespace),
		WantCreates: []metav1.Object{
			sks(testNamespace, testRevision, WithSelector(usualSelector)),
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
		},
		WantStatusUpdates: []ktesting.UpdateActionImpl{{
			Object: pa(testRevision, testNamespace, WithHPAClass, WithTraffic),
		}},
	}, {
		Name: "reconcile sks",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass, WithTraffic),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
			sks(testNamespace, testRevision, WithSelector(presources.UnionMaps(usualSelector, map[string]string{"c": "d"}))),
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
		},
		Key: key(testRevision, testNamespace),
		WantUpdates: []ktesting.UpdateActionImpl{{
			Object: sks(testNamespace, testRevision, WithSelector(usualSelector)),
		}},
	}, {
		Name: "reconcile sks - update fails",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass, WithTraffic),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
			sks(testNamespace, testRevision, WithSelector(presources.UnionMaps(usualSelector, map[string]string{"c": "d"}))),
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
		},
		Key: key(testRevision, testNamespace),
		WithReactors: []ktesting.ReactionFunc{
			InduceFailure("update", "serverlessservices"),
		},
		WantErr: true,
		WantUpdates: []ktesting.UpdateActionImpl{{
			Object: sks(testNamespace, testRevision, WithSelector(usualSelector)),
		}},
		WantEvents: []string{
			Eventf(corev1.EventTypeWarning, "InternalError", "error reconciling SKS: inducing failure for update serverlessservices"),
		},
	}, {
		Name: "create sks - create fails",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass, WithTraffic),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
		},
		Key: key(testRevision, testNamespace),
		WithReactors: []ktesting.ReactionFunc{
			InduceFailure("create", "serverlessservices"),
		},
		WantErr: true,
		WantCreates: []metav1.Object{
			sks(testNamespace, testRevision, WithSelector(usualSelector)),
		},
		WantEvents: []string{
			Eventf(corev1.EventTypeWarning, "InternalError", "error reconciling SKS: inducing failure for create serverlessservices"),
		},
	}, {
		Name: "sks is disowned",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
			sks(testNamespace, testRevision, WithSelector(usualSelector), WithSKSOwnersRemoved),
		},
		Key: key(testRevision, testNamespace),
		WantCreates: []metav1.Object{
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
		},
		WantErr: true,
		WantStatusUpdates: []ktesting.UpdateActionImpl{{
			Object: pa(testRevision, testNamespace, WithHPAClass, MarkResourceNotOwnedByPA("ServerlessService", testRevision)),
		}},
		WantEvents: []string{
			Eventf(corev1.EventTypeWarning, "InternalError", `error reconciling SKS: HPA: "test-revision" does not own SKS: "test-revision"`),
		},
	}, {
		Name: "pa is disowned",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
			sks(testNamespace, testRevision, WithSelector(usualSelector)),
			hpa(testRevision, testNamespace,
				pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"), WithPAOwnersRemoved),
				withHPAOwnersRemoved),
		},
		Key:     key(testRevision, testNamespace),
		WantErr: true,
		WantStatusUpdates: []ktesting.UpdateActionImpl{{
			Object: pa(testRevision, testNamespace, WithHPAClass, MarkResourceNotOwnedByPA("HorizontalPodAutoscaler", testRevision)),
		}},
		WantEvents: []string{
			Eventf(corev1.EventTypeWarning, "InternalError",
				`PodAutoscaler: "test-revision" does not own HPA: "test-revision"`),
		},
	}, {
		Name: "do not create hpa when non-hpa-class pod autoscaler",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithKPAClass),
		},
		Key: key(testRevision, testNamespace),
	}, {
		Name: "nop deletion reconcile",
		// Test that with a DeletionTimestamp we do nothing.
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass, WithPADeletionTimestamp),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
		},
		Key: key(testRevision, testNamespace),
	}, {
		Name: "delete hpa when pa does not exist",
		Objects: []runtime.Object{
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
		},
		Key: key(testRevision, testNamespace),
		WantDeletes: []ktesting.DeleteActionImpl{{
			ActionImpl: ktesting.ActionImpl{
				Namespace: testNamespace,
				Verb:      "delete",
				Resource: schema.GroupVersionResource{
					Group:    "autoscaling",
					Version:  "v1",
					Resource: "horizontalpodautoscalers",
				},
			},
			Name: testRevision,
		}},
	}, {
		Name:    "attempt to delete non-existent hpa when pa does not exist",
		Objects: []runtime.Object{},
		Key:     key(testRevision, testNamespace),
		WantDeletes: []ktesting.DeleteActionImpl{{
			ActionImpl: ktesting.ActionImpl{
				Namespace: testNamespace,
				Verb:      "delete",
				Resource: schema.GroupVersionResource{
					Group:    "autoscaling",
					Version:  "v1",
					Resource: "horizontalpodautoscalers",
				},
			},
			Name: testRevision,
		}},
	}, {
		Name: "failure to delete hpa",
		Objects: []runtime.Object{
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
		},
		Key: key(testRevision, testNamespace),
		WantDeletes: []ktesting.DeleteActionImpl{{
			ActionImpl: ktesting.ActionImpl{
				Namespace: testNamespace,
				Verb:      "delete",
				Resource: schema.GroupVersionResource{
					Group:    "autoscaling",
					Version:  "v1",
					Resource: "horizontalpodautoscalers",
				},
			},
			Name: testRevision,
		}},
		WithReactors: []ktesting.ReactionFunc{
			InduceFailure("delete", "horizontalpodautoscalers"),
		},
		WantErr: true,
	}, {
		Name: "update hpa with target usage",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass, WithTraffic, WithTargetAnnotation("1")),
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
			sks(testNamespace, testRevision, WithSelector(usualSelector)),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
		},
		Key: key(testRevision, testNamespace),
		WantUpdates: []ktesting.UpdateActionImpl{{
			Object: hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithTargetAnnotation("1"), WithMetricAnnotation("cpu"))),
		}},
	}, {
		Name: "invalid key",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass),
		},
		Key: "sandwich///",
	}, {
		Name: "failure to create hpa",
		Objects: []runtime.Object{
			pa(testRevision, testNamespace, WithHPAClass),
			scaleResource(testNamespace, testRevision, withLabelSelector("a=b")),
		},
		Key: key(testRevision, testNamespace),
		WantCreates: []metav1.Object{
			hpa(testRevision, testNamespace, pa(testRevision, testNamespace, WithHPAClass, WithMetricAnnotation("cpu"))),
		},
		WithReactors: []ktesting.ReactionFunc{
			InduceFailure("create", "horizontalpodautoscalers"),
		},
		WantStatusUpdates: []ktesting.UpdateActionImpl{{
			Object: pa(testRevision, testNamespace, WithHPAClass, WithNoTraffic(
				"FailedCreate", "Failed to create HorizontalPodAutoscaler \"test-revision\".")),
		}},
		WantErr: true,
		WantEvents: []string{
			Eventf(corev1.EventTypeWarning, "InternalError", "inducing failure for create horizontalpodautoscalers"),
		},
	}}

	defer ClearAllLoggers()
	table.Test(t, MakeFactory(func(listers *Listers, opt reconciler.Options) controller.Reconciler {
		return &Reconciler{
			Base:           reconciler.NewBase(opt, controllerAgentName),
			paLister:       listers.GetPodAutoscalerLister(),
			sksLister:      listers.GetServerlessServiceLister(),
			hpaLister:      listers.GetHorizontalPodAutoscalerLister(),
			scaleClientSet: opt.ScaleClientSet,
		}
	}))
}

func sks(ns, n string, so ...SKSOption) *nv1a1.ServerlessService {
	hpa := pa(n, ns, WithHPAClass)
	s := aresources.MakeSKS(hpa, map[string]string{}, nv1a1.SKSOperationModeServe)
	for _, opt := range so {
		opt(s)
	}
	return s
}

func key(name, namespace string) string {
	return namespace + "/" + name
}

func pa(name, namespace string, options ...PodAutoscalerOption) *autoscalingv1alpha1.PodAutoscaler {
	pa := &autoscalingv1alpha1.PodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: autoscalingv1alpha1.PodAutoscalerSpec{
			ScaleTargetRef: autoscalingv1.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name + "-deployment",
			},
			ServiceName:  name + "-service",
			ProtocolType: networking.ProtocolHTTP1,
		},
	}
	for _, opt := range options {
		opt(pa)
	}
	return pa
}

type hpaOption func(*autoscalingv1.HorizontalPodAutoscaler)

func withHPAOwnersRemoved(hpa *autoscalingv1.HorizontalPodAutoscaler) {
	hpa.OwnerReferences = nil
}

func hpa(name, namespace string, pa *autoscalingv1alpha1.PodAutoscaler, options ...hpaOption) *autoscalingv1.HorizontalPodAutoscaler {
	h := resources.MakeHPA(pa)
	for _, o := range options {
		o(h)
	}
	return h
}

type scaleOpt func(*autoscalingv1.Scale)

func scaleResource(namespace, name string, opts ...scaleOpt) *autoscalingv1.Scale {
	s := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: autoscalingv1.ScaleSpec{
			Replicas: 1,
		},
		Status: autoscalingv1.ScaleStatus{
			Replicas: 42,
			Selector: "a=b",
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func withLabelSelector(selector string) scaleOpt {
	return func(s *autoscalingv1.Scale) {
		s.Status.Selector = selector
	}
}
