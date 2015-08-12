package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/samalba/dockerclient"
	"github.com/satori/go.uuid"
)

// Callback used to listen to Docker's events
func eventCallback(event *dockerclient.Event, ec chan error, args ...interface{}) {
	log.Printf("Received event: %#v\n", *event)
}

type Container struct {
	Name      string
	ID        string
	Image     string
	IP        string
	Proxy     http.Handler
	Started   bool
	StartedAt time.Time
	TTL       time.Duration
	Config    *dockerclient.ContainerConfig
}

var (
	containerPrefix = "ephemera"
	docker          *dockerclient.DockerClient
	mu              sync.Mutex
	containers      = map[string]*Container{}
	dockerDebug     = false
)

func UUID() string {
	return uuid.NewV4().String()
}

func NewContainer(img string, ttl time.Duration) *Container {
	mu.Lock()
	defer mu.Unlock()
	container := &Container{
		Name:    UUID(),
		Image:   img,
		TTL:     ttl,
		Started: false,
		Config: &dockerclient.ContainerConfig{
			Image: img,
			//	Image: "fiorix/freegeoip",
		},
	}
	containers[container.Name] = container
	return container
}

func (c *Container) WaitKill() {
	<-time.After(c.TTL)
	c.Kill()
	return
}
func (c *Container) String() string {
	return fmt.Sprintf("<Container %v [img=%v,started=%v,ttl=%v]>", c.Name, c.Config.Image, c.Started, c.TTL)
}

func (c *Container) Start() {
	if c.Started {
		return
	}
	containerId, err := docker.CreateContainer(c.Config, fmt.Sprintf("%v-%v", containerPrefix, c.Name))
	if err != nil {
		log.Fatal(err)
	}
	// Start the container
	hostConfig := &dockerclient.HostConfig{}
	err = docker.StartContainer(containerId, hostConfig)
	if err != nil {
		log.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	info, _ := docker.InspectContainer(containerId)
	c.IP = info.NetworkSettings.IPAddress
	c.ID = containerId
	c.Started = true
	c.StartedAt = time.Now()
}

func (c *Container) Kill() {
	mu.Lock()
	defer mu.Unlock()
	docker.StopContainer(c.ID, 5)
	docker.RemoveContainer(c.ID, true, true)
	delete(containers, c.Name)
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	log.Println("/app/%v requested", id)
	if c, ok := containers[id]; ok {
		c.Proxy.ServeHTTP(w, r)
		return
	}
	log.Printf("unknown id %v", id)
}
func newHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("New container request")
	c := NewContainer("fiorix/freegeoip", 60*time.Second)
	c.Start()
	log.Printf("container started: %v", c)
	go c.WaitKill()
	u, _ := url.Parse(fmt.Sprintf("http://%v:8080", c.IP))
	c.Proxy = http.StripPrefix("/app/"+c.Name, httputil.NewSingleHostReverseProxy(u))
	log.Printf("container proxy setup /app/%v => %v", c.Name, c.IP)
	http.Redirect(w, r, "/app/"+c.Name, http.StatusTemporaryRedirect)
	return
}

func main() {
	// Init the Docker client
	docker, _ = dockerclient.NewDockerClient("unix:///var/run/docker.sock", nil)
	if dockerDebug {
		docker.StartMonitorEvents(eventCallback, nil)
	}
	r := mux.NewRouter()
	r.StrictSlash(true)
	r.HandleFunc("/app/new", newHandler)
	r.PathPrefix("/app/{id}").Handler(http.HandlerFunc(proxyHandler))
	http.Handle("/", r)
	go func() {
		if err := http.ListenAndServe(":8081", nil); err != nil {
			log.Fatal("ListenAndServe: ", err)
		}
	}()
	sc := make(chan os.Signal, 1)
	signal.Notify(sc,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		syscall.SIGKILL,
		os.Interrupt)
	<-sc
	for _, c := range containers {
		log.Printf("kill %v", c)
		c.Kill()
	}
}
