package ephemera

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/samalba/dockerclient"
	"github.com/satori/go.uuid"
)

var (
	containerPrefix = "ephemera"
	dockerDebug     = false
)

// Callback used to listen to Docker's events
func eventCallback(event *dockerclient.Event, ec chan error, args ...interface{}) {
	log.Printf("Received event: %#v\n", *event)
}

// Container represents a ephemeral container.
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
	e         *Ephemera
}

// WaitKill blocks till the TTL is elapsed and kill the container.
func (c *Container) WaitKill() {
	<-time.After(c.TTL)
	c.Kill()
	return
}

// String implements fmt.Stringer
func (c *Container) String() string {
	return fmt.Sprintf("<Container %v [img=%v,started=%v,ttl=%v]>", c.Name, c.Config.Image, c.Started, c.TTL)
}

// Start actually start the container
func (c *Container) Start() {
	if c.Started {
		return
	}
	containerId, err := c.e.docker.CreateContainer(c.Config, fmt.Sprintf("%v-%v", containerPrefix, c.Name))
	if err != nil {
		log.Fatal(err)
	}
	// Start the container
	hostConfig := &dockerclient.HostConfig{}
	err = c.e.docker.StartContainer(containerId, hostConfig)
	if err != nil {
		log.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	info, _ := c.e.docker.InspectContainer(containerId)
	c.IP = info.NetworkSettings.IPAddress
	c.ID = containerId
	c.Started = true
	c.StartedAt = time.Now()
}

// Kill stops and removes the container.
func (c *Container) Kill() {
	c.e.Lock()
	defer c.e.Unlock()
	c.e.docker.StopContainer(c.ID, 5)
	c.e.docker.RemoveContainer(c.ID, true, true)
	delete(c.e.containers, c.Name)
}

type Ephemera struct {
	sync.Mutex
	ttl        time.Duration
	image      string
	containers map[string]*Container
	docker     *dockerclient.DockerClient
	handler    http.Handler
}

// KillAll kills all the spawned containers still alive.
func (e *Ephemera) KillAll() {
	for _, c := range e.containers {
		log.Printf("kill %v", c)
		c.Kill()
	}
}

// RegisterHandler registers /demo/new and /demo/{id} routes.
func (e *Ephemera) RegisterHandler(r *mux.Router) {
	r.HandleFunc("/demo/new", e.newHandler)
	r.PathPrefix("/demo/{id}").Handler(http.HandlerFunc(e.proxyHandler))
}

// Spawn a new container with the given Docker image and TTL.
// The container will be killed only if WaitKill/Kill is called manually.
func (e *Ephemera) NewContainer(img string, ttl time.Duration) *Container {
	e.Lock()
	defer e.Unlock()
	container := &Container{
		e:       e,
		Name:    uuid.NewV4().String(),
		Image:   img,
		TTL:     ttl,
		Started: false,
		Config: &dockerclient.ContainerConfig{
			Image: img,
		},
	}
	e.containers[container.Name] = container
	return container
}

func (e *Ephemera) proxyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	log.Println("/demo/%v requested", id)
	if c, ok := e.containers[id]; ok {
		c.Proxy.ServeHTTP(w, r)
		return
	}
	log.Printf("unknown id %v", id)
}

func (e *Ephemera) newHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("New container request")
	c := e.NewContainer(e.image, e.ttl)
	c.Start()
	log.Printf("container started: %v", c)
	go c.WaitKill()
	u, _ := url.Parse(fmt.Sprintf("http://%v:8080", c.IP))
	c.Proxy = http.StripPrefix("/demo/"+c.Name, httputil.NewSingleHostReverseProxy(u))
	log.Printf("container proxy setup /demo/%v => %v", c.Name, c.IP)
	if r.URL.Query().Get("redirect") != "0" {
		http.Redirect(w, r, "/demo/"+c.Name, http.StatusTemporaryRedirect)
		return
	}
	w.Write([]byte(c.Name))
	return
}

// New initializes a new Ephemera instance.
func New(dockerURI, image string, ttl time.Duration) (*Ephemera, error) {
	if dockerURI == "" {
		dockerURI = "unix:///var/run/docker.sock"
	}
	// Init the Docker client
	docker, err := dockerclient.NewDockerClient(dockerURI, nil)
	if err != nil {
		return nil, err
	}
	if dockerDebug {
		docker.StartMonitorEvents(eventCallback, nil)
	}
	return &Ephemera{
		containers: map[string]*Container{},
		docker:     docker,
		ttl:        ttl,
		image:      image,
	}, nil
}
