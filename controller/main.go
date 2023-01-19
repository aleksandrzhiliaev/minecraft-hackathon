package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	minecraftSocketAddress = "116.202.8.204:3567"
	minecraftSocketPath    = "/v1/ws/console"
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

	// react on minecraft events in separate goroutine
	go func() {
		for {
			err = kubeReactor(clientset)
			if err != nil {
				log.Println("kubeReactor error:", err)
			}
		}
	}()

	//observe k8s pods and namespaces in main goroutine
	for {
		err = kubeObserver(clientset)
		if err != nil {
			log.Println("kubeObserver error:", err)
		}
		time.Sleep(1 * time.Minute)
	}
}

func kubeObserver(clientset *kubernetes.Clientset) error {
	// Idea:
	// 1. Get labeled ns
	// 2. Get pods from namespace
	// 3. Send summon command for each pod
	namespaces, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{
		LabelSelector: "flinktoid=true",
	})
	if err != nil {
		return fmt.Errorf("failed to list namespaces: %w", err)
	}

	// init websocket connection begin
	u := url.URL{Scheme: "ws", Host: minecraftSocketAddress, Path: minecraftSocketPath}
	log.Printf("connecting to %s", u.String())
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

		for _, pod := range pods.Items {
			// summon reference: https://minecraft.fandom.com/wiki/Commands/summon
			err = c.WriteMessage(websocket.TextMessage, []byte(`/summon pig 94 64 -44 {CustomName:"\"`+pod.Name+`\"",CustomNameVisible:1}`))
			if err != nil {
				return fmt.Errorf("failed to send summon command to minecraft: %w", err)
			}
		}
	}
	return nil
}

func kubeReactor(clientset *kubernetes.Clientset) error {
	// Idea:
	// 1. Watch websocket from minecraft server
	// 2. Parse message and do some action

	// init websocket connection begin
	u := url.URL{Scheme: "ws", Host: minecraftSocketAddress, Path: minecraftSocketPath}
	log.Printf("connecting to %s", u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to dial websocket : %w", err)
	}

	// watch websocket from minecraft server
	log.Println("watching websocket events...")
	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			return fmt.Errorf("failed to read message from websocket: %w", err)
		}

		//message := []byte(`{"message": "Named entity EntityCow['default_nginx'/374, uuid='71e6341b-4667-4100-bc91-7e7825078df3', l='ServerLevel[world]', x=8.64, y=86.00, z=7.96, cpos=[0, 0], tl=51, v=true] died: default_nginx was slain by AranelSurion", "timestampMillis": 1631834015918, "loggerName": "", "level": "INFO"}`)
		ns, pod, err := parseMinecraftUserKillMessage(message)
		if err != nil {
			log.Println("failed to parse minecraft message:", err)
			continue
		}
		if ns != "" {
			log.Println("parsed ns:", ns, "pod:", pod)
			// call k8s to delete pod in ns
			err = clientset.CoreV1().Pods(ns).Delete(context.Background(), pod, metav1.DeleteOptions{})
			if err != nil {
				log.Println("failed to delete pod:", err)
				continue
			}
			log.Println("deleted pod:", pod)
		}
	}

	defer c.Close()
	return nil
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
		log.Println("failed to parse message:", err)
		return "", "", err
	}
	if message.Level != "INFO" {
		return "", "", nil
	}

	re := regexp.MustCompile(`(?P<ns>[a-z-0-9]+)_(?P<pod>[a-z-0-9]+) was slain by`)
	if err != nil {
		log.Println("failed to compile regexp:", err)
		return "", "", err
	}

	// get pod, namespace
	matches := re.FindStringSubmatch(message.Message)
	if len(matches) != 3 {
		log.Println("message find player and entity mismatch, skipping:", message.Message)
		return "", "", nil
	}
	namespace := matches[1]
	pod := matches[2]

	return namespace, pod, nil
}
