package e2e

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/e2e/utils"
)

const (
	disconnectCommand = "ip route add blackhole %s"
	reconnectCommand  = "ip route delete blackhole %s"
	nodeExecTimeout   = 20 * time.Second
	reconnectInterval = 300 * time.Second
)

var _ = Describe("Poison Pill E2E", func() {

	var node *v1.Node
	workers := &v1.NodeList{}
	var oldBootTime *time.Time
	var oldUID types.UID
	var apiIPs []string

	BeforeEach(func() {

		// get all things that doesn't change once only
		if node == nil {
			// get worker node(s)
			selector := labels.NewSelector()
			req, _ := labels.NewRequirement("node-role.kubernetes.io/worker", selection.Exists, []string{})
			selector = selector.Add(*req)
			Expect(k8sClient.List(context.Background(), workers, &client.ListOptions{LabelSelector: selector})).ToNot(HaveOccurred())
			Expect(len(workers.Items)).To(BeNumerically(">=", 2))

			node = &workers.Items[0]
			oldUID = node.GetUID()

			apiIPs = getApiIPs()
		} else {
			// just update the node for getting the current UID
			Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(node), node)).ToNot(HaveOccurred())
			oldUID = node.GetUID()
		}

		var err error
		oldBootTime, err = getBootTime(node)
		Expect(err).ToNot(HaveOccurred())

	})

	AfterEach(func() {
		// restart pp pods for resetting logs...
		for _, worker := range workers.Items {
			restartPPPod(&worker)
		}
		// let things settle...
		time.Sleep(30 * time.Second)
	})

	Describe("With API connectivity", func() {
		Context("creating a PPR", func() {
			// normal remediation
			// - create PPR
			// - node should reboot
			// - node should be deleted and re-created

			var ppr *v1alpha1.PoisonPillRemediation
			BeforeEach(func() {
				ppr = createPPR(node)
			})

			AfterEach(func() {
				if ppr != nil {
					_ = k8sClient.Delete(context.Background(), ppr)
				}
			})

			It("should reboot and re-create node", func() {
				// order matters
				// - because the 2nd check has a small timeout only
				checkNodeRecreate(node, oldUID)
				checkReboot(node, oldBootTime)
			})
		})
	})

	Describe("Without API connectivity", func() {
		Context("Healthy node (no PPR)", func() {

			// no api connectivity
			// a) healthy
			//    - kill connectivity on one node
			//    - wait until connection restored
			//    - verify node did not reboot and wasn't deleted
			//    - verify peer check did happen

			BeforeEach(func() {
				killApiConnection(node, apiIPs, true)
			})

			AfterEach(func() {
				// nothing to do
			})

			It("should not reboot and not re-create node", func() {
				// order matters
				// - because the 2nd check has a small timeout only
				checkNoNodeRecreate(node, oldUID)
				checkNoReboot(node, oldBootTime)

				// check logs to make sure that the actual peer health check did run
				checkPPLogs(node, []string{"failed to check api server", "Peer told me I'm healthy."})
			})
		})

		Context("Unhealthy node (with PPR)", func() {

			// no api connectivity
			// b) unhealthy
			//    - kill connectivity on one node
			//    - create PPR
			//    - verify node does reboot and and is deleted / re-created

			var ppr *v1alpha1.PoisonPillRemediation

			BeforeEach(func() {
				killApiConnection(node, apiIPs, false)
				ppr = createPPR(node)
			})

			AfterEach(func() {
				if ppr != nil {
					_ = k8sClient.Delete(context.Background(), ppr)
				}
			})

			It("should reboot and re-create node", func() {
				// order matters
				// - because node check works while api is disconnected from node, reboot check not
				// - because the 2nd check has a small timeout only
				checkNodeRecreate(node, oldUID)
				checkReboot(node, oldBootTime)

				// we can't check logs of unhealthy node anymore, check peer logs
				peer := &workers.Items[1]
				checkPPLogs(peer, []string{node.GetName(), "node is unhealthy"})
			})

		})

		Context("Healthy node (no API connection for all)", func() {

			// no api connectivity
			// c) api issue
			//    - kill connectivity on all nodes
			//    - verify node does not reboot and isn't deleted

			BeforeEach(func() {
				for i, _ := range workers.Items {
					worker := workers.Items[i]
					go func() {
						defer GinkgoRecover()
						killApiConnection(&worker, apiIPs, true)
					}()
				}

				// we can't check the boot time while it has no api connectivity, and it will not be restored by a reboot
				// so wait until connection was restored
				time.Sleep(time.Duration(reconnectInterval.Seconds()+10) * time.Second)
			})

			AfterEach(func() {
				// nothing to do
			})

			It("should not reboot and not re-create node", func() {
				// order matters
				// - because the 2nd check has a small timeout only
				checkNoNodeRecreate(node, oldUID)
				checkNoReboot(node, oldBootTime)

				// check logs to make sure that the actual peer health check did run
				checkPPLogs(node, []string{"failed to check api server", "nodes couldn't access the api-server"})
			})
		})
	})
})

func createPPR(node *v1.Node) *v1alpha1.PoisonPillRemediation {
	By("creating a PPR")
	ppr := &v1alpha1.PoisonPillRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.GetName(),
			Namespace: testNamespace,
		},
	}
	ExpectWithOffset(1, k8sClient.Create(context.Background(), ppr)).ToNot(HaveOccurred())
	return ppr
}

func getBootTime(node *v1.Node) (*time.Time, error) {
	bootTimeCommand := []string{"uptime", "-s"}
	var bootTime time.Time
	Eventually(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), nodeExecTimeout)
		defer cancel()
		bootTimeString, err := utils.ExecCommandOnNode(k8sClient, bootTimeCommand, node, ctx)
		if err != nil {
			return err
		}
		bootTime, err = time.Parse("2006-01-02 15:04:05", bootTimeString)
		if err != nil {
			return err
		}
		return nil
	}, 6*nodeExecTimeout, 10*time.Second).ShouldNot(HaveOccurred())
	return &bootTime, nil
}

func checkNodeRecreate(node *v1.Node, oldUID types.UID) {
	By("checking if node was recreated")
	logger.Info("UID", "old", oldUID)
	EventuallyWithOffset(1, func() types.UID {
		key := client.ObjectKey{
			Name: node.GetName(),
		}
		newNode := &v1.Node{}
		if err := k8sClient.Get(context.Background(), key, newNode); err != nil {
			logger.Error(err, "error getting node")
			return oldUID
		}
		newUID := newNode.GetUID()
		logger.Info("UID", "new", newUID)
		return newUID
	}, 5*time.Minute, 10*time.Second).ShouldNot(Equal(oldUID))
}

func checkReboot(node *v1.Node, oldBootTime *time.Time) {
	By("checking reboot")
	logger.Info("boot time", "old", oldBootTime)
	// Note: short timeout only because this check runs after node re-create check,
	// where already multiple minute were spent
	EventuallyWithOffset(1, func() time.Time {
		newBootTime, err := getBootTime(node)
		if err != nil {
			return time.Time{}
		}
		logger.Info("boot time", "new", newBootTime)
		return *newBootTime
	}, 2*time.Minute, 10*time.Second).Should(BeTemporally(">", *oldBootTime))
}

func killApiConnection(node *v1.Node, apiIPs []string, withReconnect bool) {
	By("killing api connectivity")

	script := composeScript(disconnectCommand, apiIPs)
	if withReconnect {
		script += fmt.Sprintf(" && sleep %s && ", strconv.Itoa(int(reconnectInterval.Seconds())))
		script += composeScript(reconnectCommand, apiIPs)
	}

	command := []string{"/bin/bash", "-c", script}

	var ctx context.Context
	var cancel context.CancelFunc
	if withReconnect {
		ctx, cancel = context.WithTimeout(context.Background(), reconnectInterval+nodeExecTimeout)
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), nodeExecTimeout)
	}
	defer cancel()
	_, err := utils.ExecCommandOnNode(k8sClient, command, node, ctx)
	// deadline exceeded is ok... the command does not return because of the killed connection
	Expect(err).To(
		Or(
			Not(HaveOccurred()),
			WithTransform(func(err error) string { return err.Error() },
				ContainSubstring("deadline exceeded"),
			),
		),
	)
}

func composeScript(commandTemplate string, ips []string) string {
	script := ""
	for i, ip := range ips {
		if i != 0 {
			script += " && "
		}
		script += fmt.Sprintf(commandTemplate, ip)
	}
	return script
}

func checkNoNodeRecreate(node *v1.Node, oldUID types.UID) {
	By("checking if node was recreated")
	logger.Info("UID", "old", oldUID)
	// Note: short timeout because this check runs after api connection was restored,
	// and multiple minutes were spent already on this test
	EventuallyWithOffset(1, func() types.UID {
		key := client.ObjectKey{
			Name: node.GetName(),
		}
		newNode := &v1.Node{}
		if err := k8sClient.Get(context.Background(), key, newNode); err != nil {
			logger.Error(err, "error getting node")
			return "xxx"
		}
		newUID := newNode.GetUID()
		logger.Info("UID", "new", newUID)
		return newUID
	}, 1*time.Minute, 10*time.Second).Should(Equal(oldUID))
}

func checkNoReboot(node *v1.Node, oldBootTime *time.Time) {
	By("checking no reboot")
	logger.Info("boot time", "old", oldBootTime)
	// Note: short timeout because this check runs after api connection was restored,
	// and multiple minutes were spent already on this test
	// we still need Eventually because getting the boot time might still fail after fiddling with api connectivity
	EventuallyWithOffset(1, func() time.Time {
		newBootTime, err := getBootTime(node)
		if err != nil {
			return time.Time{}
		}
		logger.Info("boot time", "new", newBootTime)
		return *newBootTime
	}, 1*time.Minute, 10*time.Second).Should(BeTemporally("==", *oldBootTime))
}

func checkPPLogs(node *v1.Node, expected []string) {
	By("checking logs")
	pod := findPPPod(node)
	ExpectWithOffset(1, pod).ToNot(BeNil())

	logs := ""
	Eventually(func() string {
		var err error
		logs, err = utils.GetLogs(k8sClientSet, pod)
		if err != nil {
			return ""
		}
		return logs
	}, 3*time.Minute, 10*time.Second).ShouldNot(BeEmpty(), "failed to get logs")

	for _, exp := range expected {
		Expect(logs).To(ContainSubstring(exp), "logs don t contain expected string, did the pod restart?")
	}
}

func findPPPod(node *v1.Node) *v1.Pod {
	pods := &v1.PodList{}
	ExpectWithOffset(2, k8sClient.List(context.Background(), pods)).ToNot(HaveOccurred())
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.GetName(), "poison-pill-ds") && pod.Spec.NodeName == node.GetName() {
			return &pod
		}
	}
	return nil
}

func restartPPPod(node *v1.Node) {
	By("restarting pp pod for resetting logs")
	pod := findPPPod(node)
	ExpectWithOffset(1, pod).ToNot(BeNil())
	ExpectWithOffset(1, k8sClient.Delete(context.Background(), pod))

	// wait for restart
	oldPodUID := pod.GetUID()
	EventuallyWithOffset(1, func() types.UID {
		newPod := findPPPod(node)
		if newPod == nil {
			return oldPodUID
		}
		return newPod.GetUID()
	}, 2*time.Minute, 10*time.Second).ShouldNot(Equal(oldPodUID))
}

func getApiIPs() []string {
	key := client.ObjectKey{
		Namespace: "default",
		Name:      "kubernetes",
	}
	ep := &v1.Endpoints{}
	ExpectWithOffset(1, k8sClient.Get(context.Background(), key, ep)).ToNot(HaveOccurred())
	ips := make([]string, 0)
	for _, addr := range ep.Subsets[0].Addresses {
		ips = append(ips, addr.IP)
	}
	return ips
}
