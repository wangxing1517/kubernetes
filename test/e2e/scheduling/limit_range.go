/*
Copyright 2015 The Kubernetes Authors.

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

package scheduling

import (
	"fmt"
	"reflect"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"

	"github.com/onsi/ginkgo"
)

const (
	podName = "pfpod"
)

var _ = SIGDescribe("LimitRange", func() {
	f := framework.NewDefaultFramework("limitrange")

	ginkgo.It("should create a LimitRange with defaults and ensure pod has those defaults applied.", func() {
		ginkgo.By("Creating a LimitRange")

		min := getResourceList("50m", "100Mi", "100Gi")
		max := getResourceList("500m", "500Mi", "500Gi")
		defaultLimit := getResourceList("500m", "500Mi", "500Gi")
		defaultRequest := getResourceList("100m", "200Mi", "200Gi")
		maxLimitRequestRatio := v1.ResourceList{}
		limitRange := newLimitRange("limit-range", v1.LimitTypeContainer,
			min, max,
			defaultLimit, defaultRequest,
			maxLimitRequestRatio)

		ginkgo.By("Setting up watch")
		selector := labels.SelectorFromSet(labels.Set(map[string]string{"name": limitRange.Name}))
		options := metav1.ListOptions{LabelSelector: selector.String()}
		limitRanges, err := f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).List(options)
		framework.ExpectNoError(err, "failed to query for limitRanges")
		framework.ExpectEqual(len(limitRanges.Items), 0)
		options = metav1.ListOptions{
			LabelSelector:   selector.String(),
			ResourceVersion: limitRanges.ListMeta.ResourceVersion,
		}
		w, err := f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).Watch(metav1.ListOptions{})
		framework.ExpectNoError(err, "failed to set up watch")

		ginkgo.By("Submitting a LimitRange")
		limitRange, err = f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).Create(limitRange)
		framework.ExpectNoError(err)

		ginkgo.By("Verifying LimitRange creation was observed")
		select {
		case event, _ := <-w.ResultChan():
			if event.Type != watch.Added {
				e2elog.Failf("Failed to observe pod creation: %v", event)
			}
		case <-time.After(framework.ServiceRespondingTimeout):
			e2elog.Failf("Timeout while waiting for LimitRange creation")
		}

		ginkgo.By("Fetching the LimitRange to ensure it has proper values")
		limitRange, err = f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).Get(limitRange.Name, metav1.GetOptions{})
		framework.ExpectNoError(err)
		expected := v1.ResourceRequirements{Requests: defaultRequest, Limits: defaultLimit}
		actual := v1.ResourceRequirements{Requests: limitRange.Spec.Limits[0].DefaultRequest, Limits: limitRange.Spec.Limits[0].Default}
		err = equalResourceRequirement(expected, actual)
		framework.ExpectNoError(err)

		ginkgo.By("Creating a Pod with no resource requirements")
		pod := f.NewTestPod("pod-no-resources", v1.ResourceList{}, v1.ResourceList{})
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
		framework.ExpectNoError(err)

		ginkgo.By("Ensuring Pod has resource requirements applied from LimitRange")
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(pod.Name, metav1.GetOptions{})
		framework.ExpectNoError(err)
		for i := range pod.Spec.Containers {
			err = equalResourceRequirement(expected, pod.Spec.Containers[i].Resources)
			if err != nil {
				// Print the pod to help in debugging.
				e2elog.Logf("Pod %+v does not have the expected requirements", pod)
				framework.ExpectNoError(err)
			}
		}

		ginkgo.By("Creating a Pod with partial resource requirements")
		pod = f.NewTestPod("pod-partial-resources", getResourceList("", "150Mi", "150Gi"), getResourceList("300m", "", ""))
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
		framework.ExpectNoError(err)

		ginkgo.By("Ensuring Pod has merged resource requirements applied from LimitRange")
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(pod.Name, metav1.GetOptions{})
		framework.ExpectNoError(err)
		// This is an interesting case, so it's worth a comment
		// If you specify a Limit, and no Request, the Limit will default to the Request
		// This means that the LimitRange.DefaultRequest will ONLY take affect if a container.resources.limit is not supplied
		expected = v1.ResourceRequirements{Requests: getResourceList("300m", "150Mi", "150Gi"), Limits: getResourceList("300m", "500Mi", "500Gi")}
		for i := range pod.Spec.Containers {
			err = equalResourceRequirement(expected, pod.Spec.Containers[i].Resources)
			if err != nil {
				// Print the pod to help in debugging.
				e2elog.Logf("Pod %+v does not have the expected requirements", pod)
				framework.ExpectNoError(err)
			}
		}

		ginkgo.By("Failing to create a Pod with less than min resources")
		pod = f.NewTestPod(podName, getResourceList("10m", "50Mi", "50Gi"), v1.ResourceList{})
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
		framework.ExpectError(err)

		ginkgo.By("Failing to create a Pod with more than max resources")
		pod = f.NewTestPod(podName, getResourceList("600m", "600Mi", "600Gi"), v1.ResourceList{})
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
		framework.ExpectError(err)

		ginkgo.By("Updating a LimitRange")
		newMin := getResourceList("9m", "49Mi", "49Gi")
		limitRange.Spec.Limits[0].Min = newMin
		limitRange, err = f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).Update(limitRange)
		framework.ExpectNoError(err)

		ginkgo.By("Verifying LimitRange updating is effective")
		err = wait.Poll(time.Second*2, time.Second*20, func() (bool, error) {
			limitRange, err = f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).Get(limitRange.Name, metav1.GetOptions{})
			framework.ExpectNoError(err)
			return reflect.DeepEqual(limitRange.Spec.Limits[0].Min, newMin), nil
		})
		framework.ExpectNoError(err)

		ginkgo.By("Creating a Pod with less than former min resources")
		pod = f.NewTestPod(podName, getResourceList("10m", "50Mi", "50Gi"), v1.ResourceList{})
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
		framework.ExpectNoError(err)

		ginkgo.By("Failing to create a Pod with more than max resources")
		pod = f.NewTestPod(podName, getResourceList("600m", "600Mi", "600Gi"), v1.ResourceList{})
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
		framework.ExpectError(err)

		ginkgo.By("Deleting a LimitRange")
		err = f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).Delete(limitRange.Name, metav1.NewDeleteOptions(30))
		framework.ExpectNoError(err)

		ginkgo.By("Verifying the LimitRange was deleted")
		err = wait.Poll(time.Second*5, time.Second*30, func() (bool, error) {
			selector := labels.SelectorFromSet(labels.Set(map[string]string{"name": limitRange.Name}))
			options := metav1.ListOptions{LabelSelector: selector.String()}
			limitRanges, err := f.ClientSet.CoreV1().LimitRanges(f.Namespace.Name).List(options)

			if err != nil {
				e2elog.Logf("Unable to retrieve LimitRanges: %v", err)
				return false, nil
			}

			if len(limitRanges.Items) == 0 {
				e2elog.Logf("limitRange is already deleted")
				return true, nil
			}

			if len(limitRanges.Items) > 0 {
				if limitRanges.Items[0].ObjectMeta.DeletionTimestamp == nil {
					e2elog.Logf("deletion has not yet been observed")
					return false, nil
				}
				return true, nil
			}

			return false, nil

		})
		framework.ExpectNoError(err, "kubelet never observed the termination notice")

		ginkgo.By("Creating a Pod with more than former max resources")
		pod = f.NewTestPod(podName+"2", getResourceList("600m", "600Mi", "600Gi"), v1.ResourceList{})
		pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
		framework.ExpectNoError(err)
	})

})

func equalResourceRequirement(expected v1.ResourceRequirements, actual v1.ResourceRequirements) error {
	e2elog.Logf("Verifying requests: expected %v with actual %v", expected.Requests, actual.Requests)
	err := equalResourceList(expected.Requests, actual.Requests)
	if err != nil {
		return err
	}
	e2elog.Logf("Verifying limits: expected %v with actual %v", expected.Limits, actual.Limits)
	err = equalResourceList(expected.Limits, actual.Limits)
	return err
}

func equalResourceList(expected v1.ResourceList, actual v1.ResourceList) error {
	for k, v := range expected {
		if actualValue, found := actual[k]; !found || (v.Cmp(actualValue) != 0) {
			return fmt.Errorf("resource %v expected %v actual %v", k, v.String(), actualValue.String())
		}
	}
	for k, v := range actual {
		if expectedValue, found := expected[k]; !found || (v.Cmp(expectedValue) != 0) {
			return fmt.Errorf("resource %v expected %v actual %v", k, expectedValue.String(), v.String())
		}
	}
	return nil
}

func getResourceList(cpu, memory string, ephemeralStorage string) v1.ResourceList {
	res := v1.ResourceList{}
	if cpu != "" {
		res[v1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		res[v1.ResourceMemory] = resource.MustParse(memory)
	}
	if ephemeralStorage != "" {
		res[v1.ResourceEphemeralStorage] = resource.MustParse(ephemeralStorage)
	}
	return res
}

// newLimitRange returns a limit range with specified data
func newLimitRange(name string, limitType v1.LimitType,
	min, max,
	defaultLimit, defaultRequest,
	maxLimitRequestRatio v1.ResourceList) *v1.LimitRange {
	return &v1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.LimitRangeSpec{
			Limits: []v1.LimitRangeItem{
				{
					Type:                 limitType,
					Min:                  min,
					Max:                  max,
					Default:              defaultLimit,
					DefaultRequest:       defaultRequest,
					MaxLimitRequestRatio: maxLimitRequestRatio,
				},
			},
		},
	}
}
