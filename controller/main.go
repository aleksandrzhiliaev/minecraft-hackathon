package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	minecraftSocketAddress   = "116.202.8.204:4567"
	minecraftSocketPath      = "/v1/ws/console"
	initialPodList           = []string{}
	createdMinecraftEntities = map[string]bool{}
)

func main() {
	home := homedir.HomeDir()
	config, err := clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	// initial pod list when first time started
	podList(clientset)
	fmt.Println("Initial Pod List:", initialPodList)

	var wg sync.WaitGroup

	// sync k8s pods from minecraft
	wg.Add(1)
	go func() {
		for {
			err = kubeReactor(clientset)
			if err != nil {
				log.Println("kubeReactor error:", err)
			}
		}
		wg.Done()
	}()

	// sync minecraft world from k8s
	wg.Add(1)
	go func() {
		for {
			err = kubeObserver(clientset)
			if err != nil {
				log.Println("kubeObserver error:", err)
			}
			time.Sleep(30 * time.Second)
		}
		wg.Done()
	}()

	wg.Wait()
	log.Println("Finishing controller")
}

func podList(clientset *kubernetes.Clientset) {
	namespaces, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{
		//LabelSelector: "kubernetes.io/metadata.name=hub",
		LabelSelector: "flinktoid=true",
	})
	if err != nil {
		panic(err)
	}

	for _, ns := range namespaces.Items {
		pods, err := clientset.CoreV1().Pods(ns.Name).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err)
		}

		initialPodList = []string{}
		for _, pod := range pods.Items {
			initialPodList = append(initialPodList, pod.Name)
		}
	}

}

func kubeObserver(clientset *kubernetes.Clientset) error {
	// Idea:
	// 1. Get labeled ns
	// 2. Get pods from namespace
	// 3. Send summon command for each pod
	entityType := []string{"pig", "cow", "turtle"}
	namespaces, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{
		//LabelSelector: "kubernetes.io/metadata.name=hub",
		LabelSelector: "flinktoid=true",
	})
	if err != nil {
		return fmt.Errorf("failed to list namespaces: %w", err)
	}

	u := url.URL{Scheme: "ws", Host: minecraftSocketAddress, Path: minecraftSocketPath}
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to dial websocket : %w", err)
	}
	defer c.Close()

	// cleanup world first
	err = c.WriteMessage(websocket.TextMessage, []byte("/kill @e[type=item]"))
	if err != nil {
		return fmt.Errorf("failed to send kill command to minecraft: %w", err)
	}

	for _, ns := range namespaces.Items {
		pods, err := clientset.CoreV1().Pods(ns.Name).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("failed to list pods: %w", err)
		}

		fmt.Println("Namespace:", ns.Name)

		podSlice := []string{}
		for _, pod := range pods.Items {
			// summon reference: https://minecraft.fandom.com/wiki/Commands/summon
			entityName := fmt.Sprintf("%s_%s", pod.Namespace, pod.Name)
			// check if entityName not in createdMinecraftEntities map, so we don't populate world on each run
			if _, ok := createdMinecraftEntities[entityName]; ok {
				continue
			}

			selected := entityType[rand.Intn(len(entityType))]
			summonCommand := `/summon ` + selected + ` -201 64 -499 {CustomName:"\"` + entityName + `\"",CustomNameVisible:1}`
			fmt.Println(summonCommand)
			err = c.WriteMessage(websocket.TextMessage, []byte(summonCommand))
			if err != nil {
				return fmt.Errorf("failed to send summon command to minecraft: %w", err)
			}
			podSlice = append(podSlice, pod.Name)
			createdMinecraftEntities[entityName] = true
			log.Println("created new entity", entityName)
		}
		fmt.Println("Pod list:", podSlice)

		difference := make(map[string]struct{}, len(podSlice))
		for _, x := range podSlice {
			difference[x] = struct{}{}
		}
		var diff []string
		for _, x := range initialPodList {
			if _, found := difference[x]; !found {
				diff = append(diff, x)
			}
		}
		fmt.Println("Difference:", diff)

		for _, toKill := range diff {
			if len(diff) > 0 {
				fmt.Println("To Kill:", toKill)
				err = c.WriteMessage(websocket.TextMessage, []byte(`/kill @e[name="\"`+toKill+`\""]`))
				if err != nil {
					panic(err)
				}
				fmt.Println("Killed:", toKill)
			}
		}

		initialPodList = podSlice
		fmt.Println("Updated Pod List:", initialPodList)
	}
	return nil
}

func kubeReactor(clientset *kubernetes.Clientset) error {
	// Idea:
	// 1. Watch websocket from minecraft server
	// 2. Parse message and do some action

	u := url.URL{Scheme: "ws", Host: minecraftSocketAddress, Path: minecraftSocketPath}
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to dial websocket : %w", err)
	}
	defer c.Close()

	// watch websocket from minecraft server
	log.Println("watching websocket events...")
	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			return fmt.Errorf("failed to read message from websocket: %w", err)
		}

		handleMinecraftKillMessage(message, clientset)
	}
}

func handleMinecraftKillMessage(message []byte, clientset *kubernetes.Clientset) {
	// Message example:
	//message := []byte(`{"message": "Named entity EntityCow['default_nginx'/374, uuid='71e6341b-4667-4100-bc91-7e7825078df3', l='ServerLevel[world]', x=8.64, y=86.00, z=7.96, cpos=[0, 0], tl=51, v=true] died: default_nginx was slain by AranelSurion", "timestampMillis": 1631834015918, "loggerName": "", "level": "INFO"}`)
	ns, pod, err := parseMinecraftUserKillMessage(message)
	if err != nil {
		log.Println("failed to parse minecraft message:", err)
		return
	}
	if ns != "" {
		err = clientset.CoreV1().Pods(ns).Delete(context.Background(), pod, metav1.DeleteOptions{})
		if err != nil {
			log.Println("failed to delete pod:", err)
			return
		}
		log.Println("deleted pod:", pod)
	}
}

func parseMinecraftUserKillMessage(body []byte) (string, string, error) {
	// Idea: Parse message and handle it:
	// Example: {"message": "Named entity EntityCow['default_nginx'/374, uuid='71e6341b-4667-4100-bc91-7e7825078df3', l='ServerLevel[world]', x=8.64, y=86.00, z=7.96, cpos=[0, 0], tl=51, v=true] died: default_nginx was slain by AranelSurion", "timestampMillis": 1631834015918, "loggerName": "", "level": "INFO"}

	// 1. Parse message
	type MinecraftMessage struct {
		Message         string `json:"message"`
		TimestampMillis int64  `json:"timestampMillis"`
		LoggerName      string `json:"loggerName"`
		Level           string `json:"level"`
	}
	var message = MinecraftMessage{}

	err := json.Unmarshal(body, &message)
	if err != nil {
		return "", "", fmt.Errorf("failed to unmarshal message: %w", err)
	}
	if message.Level != "INFO" {
		return "", "", nil
	}

	re := regexp.MustCompile(`(?P<ns>[a-z-0-9]+)_(?P<pod>[a-z-0-9]+) was slain by`)
	if err != nil {
		return "", "", fmt.Errorf("failed to compile regexp: %w", err)
	}

	// get pod, namespace
	matches := re.FindStringSubmatch(message.Message)
	if len(matches) != 3 {
		return "", "", nil
	}
	namespace := matches[1]
	pod := matches[2]

	return namespace, pod, nil
}
