/*
Copyright 2021.

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

package testcontroler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/controllers/tests/shared"
	"github.com/medik8s/self-node-remediation/pkg/apicheck"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	testEnv                 *envtest.Environment
	dummyDog                watchdog.Watchdog
	unhealthyNode, peerNode = &v1.Node{}, &v1.Node{}
	cancelFunc              context.CancelFunc
	k8sClient               *shared.K8sClientWrapper
	fakeRecorder            *record.FakeRecorder
)

var unhealthyNodeNamespacedName = client.ObjectKey{
	Name:      shared.UnhealthyNodeName,
	Namespace: "",
}
var peerNodeNamespacedName = client.ObjectKey{
	Name:      shared.PeerNodeName,
	Namespace: "",
}

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SNR Controller Test Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("../../..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = selfnoderemediationv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0",
	})
	Expect(err).ToNot(HaveOccurred())

	k8sClient = &shared.K8sClientWrapper{
		Client:                  k8sManager.GetClient(),
		Reader:                  k8sManager.GetAPIReader(),
		SimulatedFailureMessage: "simulation of client error for delete when listing namespace",
	}
	Expect(k8sClient).ToNot(BeNil())

	nsToCreate := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: shared.Namespace,
		},
	}

	Expect(k8sClient.Create(context.Background(), nsToCreate)).To(Succeed())
	dummyDog = watchdog.NewFake(true)
	err = k8sManager.Add(dummyDog)
	Expect(err).ToNot(HaveOccurred())
	timeToAssumeNodeRebooted := time.Duration(shared.MaxErrorThreshold) * shared.ApiCheckInterval
	timeToAssumeNodeRebooted += dummyDog.GetTimeout()
	timeToAssumeNodeRebooted += 5 * time.Second
	mockManagerCalculator := &shared.MockCalculator{MockTimeToAssumeNodeRebooted: timeToAssumeNodeRebooted, IsAgentVar: false}
	err = (&controllers.SelfNodeRemediationConfigReconciler{
		Client:                    k8sManager.GetClient(),
		Log:                       ctrl.Log.WithName("controllers").WithName("self-node-remediation-config-controller"),
		InstallFileFolder:         "../../../install/",
		Scheme:                    scheme.Scheme,
		Namespace:                 shared.Namespace,
		ManagerSafeTimeCalculator: mockManagerCalculator,
	}).SetupWithManager(k8sManager)

	// peers need their own node on start
	unhealthyNode = getNode(shared.UnhealthyNodeName)
	Expect(k8sClient.Create(context.Background(), unhealthyNode)).To(Succeed(), "failed to create unhealthy node")

	peerNode = getNode(shared.PeerNodeName)
	Expect(k8sClient.Create(context.Background(), peerNode)).To(Succeed(), "failed to create peer node")

	peerApiServerTimeout := 5 * time.Second
	peers := peers.New(shared.UnhealthyNodeName, shared.PeerUpdateInterval, k8sClient, ctrl.Log.WithName("peers"), peerApiServerTimeout)
	err = k8sManager.Add(peers)
	Expect(err).ToNot(HaveOccurred())

	rebooter := reboot.NewWatchdogRebooter(dummyDog, ctrl.Log.WithName("rebooter"))
	apiConnectivityCheckConfig := &apicheck.ApiConnectivityCheckConfig{
		Log:                ctrl.Log.WithName("api-check"),
		MyNodeName:         shared.UnhealthyNodeName,
		CheckInterval:      shared.ApiCheckInterval,
		MaxErrorsThreshold: shared.MaxErrorThreshold,
		Peers:              peers,
		Rebooter:           rebooter,
		Cfg:                cfg,
	}
	apiCheck := apicheck.New(apiConnectivityCheckConfig, nil)
	err = k8sManager.Add(apiCheck)
	Expect(err).ToNot(HaveOccurred())

	restoreNodeAfter := 5 * time.Second
	mockAgentCalculator := &shared.MockCalculator{MockTimeToAssumeNodeRebooted: timeToAssumeNodeRebooted, IsAgentVar: true}
	fakeRecorder = record.NewFakeRecorder(1000)
	// reconciler for unhealthy node
	err = (&controllers.SelfNodeRemediationReconciler{
		Client:             k8sClient,
		Log:                ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("unhealthy node"),
		Rebooter:           rebooter,
		MyNodeName:         shared.UnhealthyNodeName,
		RestoreNodeAfter:   restoreNodeAfter,
		SafeTimeCalculator: mockAgentCalculator,
		Recorder:           fakeRecorder,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	// reconciler for peer node
	err = (&controllers.SelfNodeRemediationReconciler{
		Client:             k8sClient,
		Log:                ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("peer node"),
		MyNodeName:         shared.PeerNodeName,
		Rebooter:           rebooter,
		RestoreNodeAfter:   restoreNodeAfter,
		SafeTimeCalculator: mockAgentCalculator,
		Recorder:           fakeRecorder,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	// reconciler for manager running on peer node
	err = (&controllers.SelfNodeRemediationReconciler{
		Client:             k8sClient,
		Log:                ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("manager node"),
		MyNodeName:         shared.PeerNodeName,
		Rebooter:           rebooter,
		RestoreNodeAfter:   restoreNodeAfter,
		SafeTimeCalculator: mockManagerCalculator,
		Recorder:           fakeRecorder,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	var ctx context.Context
	ctx, cancelFunc = context.WithCancel(ctrl.SetupSignalHandler())

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

})

func getNode(name string) *v1.Node {
	node := &v1.Node{}
	node.Name = name
	node.Labels = make(map[string]string)
	node.Labels["kubernetes.io/hostname"] = name

	return node
}

var _ = AfterSuite(func() {
	cancelFunc()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())

})
