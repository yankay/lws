/*
Copyright 2025.

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

package schedulerprovider

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	leaderworkerset "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

var scheme = runtime.NewScheme()

func init() {
	_ = volcanov1beta1.AddToScheme(scheme)
	_ = leaderworkerset.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
}

func TestVolcanoProvider_CreatePodGroupIfNotExists(t *testing.T) {
	testLeaderPod1 := createTestLeaderPod("test-lws-0", "default", "test-lws", "0", "abc123")
	testLeaderPod2 := createTestLeaderPod("test-lws-1", "default", "test-lws", "1", "def456")
	testLeaderPod3 := createTestLeaderPod("test-lws-0", "default", "test-lws", "0", "xyz789")
	testLeaderPod4 := createTestLeaderPod("test-lws-2", "default-1", "test-lws-1", "2", "jkl012")

	tests := []struct {
		name           string
		lws            *leaderworkerset.LeaderWorkerSet
		leaderPod      *corev1.Pod
		existingPG     *volcanov1beta1.PodGroup
		injectGetError error
		expectError    bool
		expectedPG     *volcanov1beta1.PodGroup
	}{
		{
			name: "create podgroup for LeaderCreated policy",
			lws: &leaderworkerset.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-lws", Namespace: "default"},
				Spec: leaderworkerset.LeaderWorkerSetSpec{
					StartupPolicy: leaderworkerset.LeaderCreatedStartupPolicy,
					LeaderWorkerTemplate: leaderworkerset.LeaderWorkerTemplate{
						Size: ptr.To[int32](3),
						WorkerTemplate: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "worker", Image: "nginx",
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
								}},
							},
						},
					},
				},
			},
			leaderPod:   testLeaderPod1,
			expectError: false,
			expectedPG: &volcanov1beta1.PodGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-lws-0-abc123",
					Namespace: "default",
					Labels: map[string]string{
						leaderworkerset.GroupIndexLabelKey: "0",
						leaderworkerset.SetNameLabelKey:    "test-lws",
						leaderworkerset.RevisionKey:        "abc123",
					},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(testLeaderPod1, corev1.SchemeGroupVersion.WithKind("Pod"))},
				},
				Spec: volcanov1beta1.PodGroupSpec{
					MinMember:    3,
					MinResources: &corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
				},
			},
		},
		{
			name: "create podgroup for LeaderReady policy",
			lws: &leaderworkerset.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-lws", Namespace: "default"},
				Spec: leaderworkerset.LeaderWorkerSetSpec{
					StartupPolicy: leaderworkerset.LeaderReadyStartupPolicy,
					LeaderWorkerTemplate: leaderworkerset.LeaderWorkerTemplate{
						Size: ptr.To[int32](3),
						WorkerTemplate: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "worker", Image: "nginx",
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
								}},
							},
						},
					},
				},
			},
			leaderPod:   testLeaderPod2,
			expectError: false,
			expectedPG: &volcanov1beta1.PodGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-lws-1-def456",
					Namespace: "default",
					Labels: map[string]string{
						leaderworkerset.GroupIndexLabelKey: "1",
						leaderworkerset.SetNameLabelKey:    "test-lws",
						leaderworkerset.RevisionKey:        "def456",
					},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(testLeaderPod2, corev1.SchemeGroupVersion.WithKind("Pod"))},
				},
				Spec: volcanov1beta1.PodGroupSpec{
					MinMember:    1,
					MinResources: &corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
				},
			},
		},
		{
			name: "podgroup already exists",
			lws: &leaderworkerset.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-lws", Namespace: "default"},
				Spec: leaderworkerset.LeaderWorkerSetSpec{
					LeaderWorkerTemplate: leaderworkerset.LeaderWorkerTemplate{Size: ptr.To[int32](3)},
				},
			},
			leaderPod: testLeaderPod3,
			existingPG: &volcanov1beta1.PodGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-lws-0-xyz789", Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(testLeaderPod3, corev1.SchemeGroupVersion.WithKind("Pod"))},
				},
				Spec: volcanov1beta1.PodGroupSpec{MinMember: 3},
			},
			expectError: false,
			expectedPG: &volcanov1beta1.PodGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-lws-0-xyz789", Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(testLeaderPod3, corev1.SchemeGroupVersion.WithKind("Pod"))},
				},
				Spec: volcanov1beta1.PodGroupSpec{MinMember: 3},
			},
		},
		{
			name: "create podgroup inherit volcano annotations",
			lws: &leaderworkerset.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-lws-1",
					Namespace: "default-1",
					Annotations: map[string]string{
						"volcano.sh/sla-waiting-time": "5m",
					},
				},
				Spec: leaderworkerset.LeaderWorkerSetSpec{
					LeaderWorkerTemplate: leaderworkerset.LeaderWorkerTemplate{
						Size: ptr.To[int32](3),
						WorkerTemplate: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "worker", Image: "nginx",
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
								}},
							},
						},
					},
				},
			},
			leaderPod:   testLeaderPod4,
			expectError: false,
			expectedPG: &volcanov1beta1.PodGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-lws-1-2-jkl012",
					Namespace: "default-1",
					Labels: map[string]string{
						leaderworkerset.GroupIndexLabelKey: "2",
						leaderworkerset.SetNameLabelKey:    "test-lws-1",
						leaderworkerset.RevisionKey:        "jkl012",
					},
					Annotations: map[string]string{
						"volcano.sh/sla-waiting-time": "5m",
					},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(testLeaderPod4, corev1.SchemeGroupVersion.WithKind("Pod"))},
				},
				Spec: volcanov1beta1.PodGroupSpec{
					MinMember:    3,
					MinResources: &corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
				},
			},
		},
		{
			name: "generic error on getting podgroup",
			lws: &leaderworkerset.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-lws", Namespace: "default"},
				Spec: leaderworkerset.LeaderWorkerSetSpec{
					LeaderWorkerTemplate: leaderworkerset.LeaderWorkerTemplate{Size: ptr.To[int32](3)},
				},
			},
			leaderPod:      testLeaderPod1,
			injectGetError: errors.New("unexpected error"),
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.existingPG != nil {
				objs = append(objs, tt.existingPG)
			}

			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...)
			if tt.injectGetError != nil {
				builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*volcanov1beta1.PodGroup); ok {
							return tt.injectGetError
						}
						return c.Get(ctx, key, obj)
					},
				})
			}
			fakeClient := builder.Build()

			provider := NewVolcanoProvider(fakeClient)
			err := provider.CreatePodGroupIfNotExists(context.TODO(), tt.lws, tt.leaderPod)

			if tt.expectError {
				assert.Error(t, err)
				if tt.injectGetError != nil {
					assert.Equal(t, tt.injectGetError, err)
				}
				return
			}

			assert.NoError(t, err)

			var actualPG volcanov1beta1.PodGroup
			pgName := tt.leaderPod.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey]
			err = fakeClient.Get(context.TODO(), types.NamespacedName{Name: pgName, Namespace: tt.lws.Namespace}, &actualPG)
			assert.NoError(t, err)

			opts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
			}

			if diff := cmp.Diff(tt.expectedPG, &actualPG, opts...); diff != "" {
				t.Errorf("PodGroup mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVolcanoProvider_ExistingPodGroup(t *testing.T) {
	leader := createTestLeaderPod("test-lws-0", "default", "test-lws", "0", "revision")
	lws := &leaderworkerset.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-lws", Namespace: "default"},
		Spec: leaderworkerset.LeaderWorkerSetSpec{
			LeaderWorkerTemplate: leaderworkerset.LeaderWorkerTemplate{Size: ptr.To[int32](1)},
		},
	}
	tests := []struct {
		name        string
		mutate      func(*volcanov1beta1.PodGroup)
		wantError   bool
		wantWaiting bool
	}{
		{
			name: "current leader owns the PodGroup",
		},
		{
			name: "previous leader owns the PodGroup before deletion starts",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.OwnerReferences[0].UID = "previous-leader-uid"
			},
			wantError: true, wantWaiting: true,
		},
		{
			name: "current leader owns a terminating PodGroup",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.DeletionTimestamp = ptr.To(metav1.Now())
				pg.Finalizers = []string{"test.lws.sigs.k8s.io/hold-podgroup"}
			},
			wantError: true, wantWaiting: true,
		},
		{
			name: "previous leader owns a terminating PodGroup",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.OwnerReferences[0].UID = "previous-leader-uid"
				pg.DeletionTimestamp = ptr.To(metav1.Now())
				pg.Finalizers = []string{"test.lws.sigs.k8s.io/hold-podgroup"}
			},
			wantError: true, wantWaiting: true,
		},
		{
			name: "PodGroup has no owner",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.OwnerReferences = nil
			},
			wantError: true,
		},
		{
			name: "PodGroup has no controller owner",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.OwnerReferences[0].Controller = ptr.To(false)
			},
			wantError: true,
		},
		{
			name: "PodGroup owner has an unexpected API version",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.OwnerReferences[0].APIVersion = "example.com/v1"
			},
			wantError: true,
		},
		{
			name: "PodGroup owner has an unexpected kind",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.OwnerReferences[0].Kind = "ConfigMap"
			},
			wantError: true,
		},
		{
			name: "PodGroup belongs to another Pod",
			mutate: func(pg *volcanov1beta1.PodGroup) {
				pg.OwnerReferences[0].Name = "another-leader"
			},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg := &volcanov1beta1.PodGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:            leader.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey],
					Namespace:       leader.Namespace,
					UID:             "existing-podgroup-uid",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(leader, corev1.SchemeGroupVersion.WithKind("Pod"))},
				},
				Spec: volcanov1beta1.PodGroupSpec{MinMember: 1},
			}
			if tt.mutate != nil {
				tt.mutate(pg)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pg).Build()
			var before volcanov1beta1.PodGroup
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pg), &before))

			err := NewVolcanoProvider(c).CreatePodGroupIfNotExists(context.Background(), lws, leader)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantWaiting, errors.Is(err, ErrPodGroupNotReady))
			var after volcanov1beta1.PodGroup
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pg), &after))
			assert.Empty(t, cmp.Diff(before, after), "existing PodGroups must not be adopted or modified")
		})
	}
}

func TestVolcanoProvider_CreateErrors(t *testing.T) {
	leader := createTestLeaderPod("test-lws-0", "default", "test-lws", "0", "revision")
	lws := &leaderworkerset.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-lws", Namespace: "default"},
		Spec: leaderworkerset.LeaderWorkerSetSpec{
			LeaderWorkerTemplate: leaderworkerset.LeaderWorkerTemplate{Size: ptr.To[int32](1)},
		},
	}
	tests := map[string]error{
		"cache has not observed an existing PodGroup": apierrors.NewAlreadyExists(volcanov1beta1.Resource("podgroups"), leader.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey]),
		"unexpected API failure":                      errors.New("unexpected create error"),
	}
	for name, createError := range tests {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					return createError
				},
			}).Build()
			err := NewVolcanoProvider(c).CreatePodGroupIfNotExists(context.Background(), lws, leader)
			require.ErrorIs(t, err, createError)
			assert.NotErrorIs(t, err, ErrPodGroupNotReady)
		})
	}
}

// Helper function to create test leader pods
func createTestLeaderPod(name, namespace, lwsName, groupIndex, revision string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name),
			Annotations: map[string]string{
				volcanov1beta1.KubeGroupNameAnnotationKey: GetPodGroupName(lwsName, groupIndex, revision),
			},
			Labels: map[string]string{
				leaderworkerset.SetNameLabelKey:    lwsName,
				leaderworkerset.GroupIndexLabelKey: groupIndex,
				leaderworkerset.RevisionKey:        revision,
			},
		},
	}
}
