package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	minecraftSocketAddress = "0.0.0.0:55000"
	minecraftSockerPath    = "/v1/console"
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

	for {
		kubeObserver(clientset)
		time.Sleep(1 * time.Minute)
	}

}

func kubeObserver(clientset *kubernetes.Clientset) {
	// Idea:
	// 1. Get labeled ns
	// 2. Get pods from namespace
	// 3. Send summon command for each pod
	namespaces, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{
		LabelSelector: "flinktoid=true",
	})
	if err != nil {
		panic(err)
	}

	// init websocket connection begin
	u := url.URL{Scheme: "ws", Host: minecraftSocketAddress, Path: minecraftSockerPath}
	log.Printf("connecting to %s", u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("dial:", err)
	}

	for _, ns := range namespaces.Items {
		pods, err := clientset.CoreV1().Pods(ns.Name).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err)
		}

		fmt.Println("Namespace:", ns.Name)

		for _, pod := range pods.Items {
			fmt.Println("Pod:", pod.Name)

			// summon reference: https://minecraft.fandom.com/wiki/Commands/summon
			err = c.WriteMessage(websocket.TextMessage, []byte("/summon cow ~-10 ~ ~"))
			if err != nil {
				log.Println("write for this pod:", pod.Name, err)
				return
			}
		}
	}

	// loop end, close websocket connection
	c.Close()
}
