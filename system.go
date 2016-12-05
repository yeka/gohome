package gohome

import (
	"github.com/go-home-iot/event-bus"
	"github.com/go-home-iot/upnp"
	"github.com/markdaws/gohome/feature"
	"github.com/markdaws/gohome/log"
	"github.com/nu7hatch/gouuid"
)

// SystemServices is a collection of services that devices can access
// such as UPNP notification and discovery
type SystemServices struct {
	UPNP         *upnp.SubServer
	Monitor      *Monitor
	EvtBus       *evtbus.Bus
	CmdProcessor CommandProcessor
}

// NewIDer is an interface for types that can generate globally unique IDs that
// can be used as IDs for objects in the system
type NewIDer interface {
	NewID() string
}

// System is a container that holds information such as all the zones and devices
// that have been created.
type System struct {
	Name        string
	Description string
	Area        *Area
	Devices     map[string]*Device
	Scenes      map[string]*Scene
	Features    map[string]*feature.Feature
	Users       map[string]*User
	Triggers    map[string]Trigger
	Extensions  *Extensions
	Services    SystemServices
}

// NewSystem returns an initial System instance.  It is still up to the caller
// to create all of the services and add them to the system after calling this function
func NewSystem(name, desc string) *System {
	s := &System{
		Name:        name,
		Description: desc,
		Devices:     make(map[string]*Device),
		Scenes:      make(map[string]*Scene),
		Features:    make(map[string]*feature.Feature),
		Users:       make(map[string]*User),
		Triggers:    make(map[string]Trigger),
	}

	// Area is the root area which all of the devices and features are contained within
	s.Area = &Area{
		ID:   s.NewID(),
		Name: "Home",
	}

	s.Extensions = NewExtensions()
	return s
}

// NewID returns the next unique global ID that can be used as an identifier
// for an item in the system.
func (s *System) NewID() string {
	ID, err := uuid.NewV4()
	if err != nil {
		//TODO: Fail gracefully from this, keep looping?
		panic("failed to generate unique id in call to NextGlobalID:" + err.Error())
	}
	return ID.String()
}

// InitDevices loops through all of the devices in the system and initializes them.
// This is async so after returning from this function the devices are still
// probably trying to initialize.  A device may try to create network connections
// or other tasks when it is initialized
func (s *System) InitDevices() {
	for _, d := range s.Devices {
		s.InitDevice(d)
	}
}

// InitDevice initializes a device.  If the device has a connection pool, this is
// when it will be initialized.  Also if the device produces or consumes events
// from the system bus, this is where it will be added to the event bus
func (s *System) InitDevice(d *Device) error {
	log.V("Init Device: %s", d)

	// If the device requires a connection pool, init all of the connections
	var done chan bool
	if d.Connections != nil {
		log.V("%s init connections", d)
		done = d.Connections.Init()
		_ = done
		log.V("%s connected", d)
	}

	evts := s.Extensions.FindEvents(s, d)
	if evts != nil {
		if evts.Producer != nil {
			log.V("%s - added event producer", d)
			s.Services.EvtBus.AddProducer(evts.Producer)
		}
		if evts.Consumer != nil {
			log.V("%s - added event consumer", d)
			s.Services.EvtBus.AddConsumer(evts.Consumer)
		}
	}

	return nil
}

// StopDevice stops the device, closes any network connections and any other services
// associated with the device
func (s *System) StopDevice(d *Device) {
	log.V("Stop Device: %s", d)

	if d.Connections != nil {
		d.Connections.Close()
	}

	evts := s.Extensions.FindEvents(s, d)
	if evts != nil {
		if evts.Producer != nil {
			s.Services.EvtBus.RemoveProducer(evts.Producer)
		}
		if evts.Consumer != nil {
			s.Services.EvtBus.RemoveConsumer(evts.Consumer)
		}
	}
}

func (s *System) AddFeature(f *feature.Feature) {
	s.Features[f.ID] = f
}

// ButtonGeatures returns a slice containing all of the button features in the system
func (s *System) ButtonFeatures() map[string]*feature.Feature {
	//TODO: Worth caching?

	features := make(map[string]*feature.Feature)
	for _, f := range s.Features {
		if f.Type == feature.FTButton {
			features[f.ID] = f
		}
	}
	return features
}

// AddDevice adds a device to the system
func (s *System) AddDevice(d *Device) {
	s.Devices[d.ID] = d
}

// AddScene adds a scene to the system
func (s *System) AddScene(scn *Scene) {
	s.Scenes[scn.ID] = scn
}

// DeleteScene deletes a scene from the system
func (s *System) DeleteScene(scn *Scene) {
	delete(s.Scenes, scn.ID)
}

// AddUser adds the user to the system
func (s *System) AddUser(u *User) {
	s.Users[u.ID] = u
}

// AddTrigger adds a trigger to the system
func (s *System) AddTrigger(t Trigger) {
	s.Triggers[t.GetID()] = t
}

// DeleteDevice deletes a device from the system and stops all associated
// services, for all zones and devices this is responsible for
func (s *System) DeleteDevice(d *Device) {
	delete(s.Devices, d.ID)
	//TODO: Remove all associated zones + buttons
	//TODO: Need to stop all services, recipes, networking etc to this device
}

// IsDupeDevice returns true if the device is a dupe of one the system
// already owns.  This check is not based on ID equality, since you could
// have scanned for a device previosuly and a second time, both scans will
// give a different ID for the device, since they are globally unique even
// though they are the same device
func (s *System) IsDupeDevice(x *Device) (*Device, bool) {
	for _, y := range s.Devices {
		if x.Address == y.Address {
			// Two devices are considered equal if they share the same address, however
			// if they have the same address but different hubs (if they have one) then
			// those are unique since the hub controls the device

			xHasHub := x.Hub != nil
			yHasHub := y.Hub != nil

			// Even though they have the same address, they have different hubs
			// so they are different
			if xHasHub != yHasHub {
				return nil, false
			}

			if !xHasHub && !yHasHub {
				// Both devices aren't under a hub and both have the same address so
				// they are a dupe
				return y, true
			}

			// If we are here both devices have the same address and they both have a hub
			// if the hubs are equal then the devices are equal
			return y, x.Hub.ID == y.Hub.ID
		}
	}
	return nil, false
}

type Scener interface {
	Scene(ID string) *Scene
}

func (s *System) Scene(ID string) *Scene {
	return s.Scenes[ID]
}

type CmdEnqueuer interface {
	CmdEnqueue(g CommandGroup) error
}

func (s *System) CmdEnqueue(g CommandGroup) error {
	return s.Services.CmdProcessor.Enqueue(g)
}
