package dbusext

import (
	"errors"

	"github.com/godbus/dbus"
)

// Subscribe sets up this connection to subscribe to all systemd dbus events.
// This is required before calling SubscribeUnits. When the connection closes
// systemd will automatically stop sending signals so there is no need to
// explicitly call Unsubscribe().
func (c *Conn) Subscribe() error {
	c.sigconn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0,
		"type='signal',interface='org.freedesktop.DBus.Properties',member='PropertiesChanged'")

	c.sigconn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0,
		"type='signal',interface='org.freedesktop.systemd1.Manager',member='UnitFilesChanged'")

	c.sigconn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0,
		"type='signal',interface='org.freedesktop.systemd1.Manager',member='Reloading'")

	err := c.sigobj.Call("org.freedesktop.systemd1.Manager.Subscribe", 0).Store()
	if err != nil {
		return err
	}

	return nil
}

// Unsubscribe this connection from systemd dbus events.
func (c *Conn) Unsubscribe() error {
	c.RemoveSubStateSubscriber()
	err := c.sigobj.Call("org.freedesktop.systemd1.Manager.Unsubscribe", 0).Store()
	if err != nil {
		return err
	}

	return nil
}

func (c *Conn) dispatch() {
	ch := make(chan *dbus.Signal, signalBuffer)

	c.sigconn.Signal(ch)

	go func() {
		reloadState := false
		unitFilesChanged := false
		for {
			signal, ok := <-ch
			if !ok {
				return
			}

			if signal.Name == "org.freedesktop.systemd1.Manager.JobRemoved" {
				c.jobComplete(signal)
			}

			if c.subscriber.updateCh == nil {
				continue
			}

			var unitPath dbus.ObjectPath
			switch signal.Name {
			case "org.freedesktop.systemd1.Manager.JobRemoved":
				c.sysobj.Call("org.freedesktop.systemd1.Manager.GetUnit", 0, signal.Body[2].(string)).Store(&unitPath)
			case "org.freedesktop.DBus.Properties.PropertiesChanged":
				if signal.Body[0].(string) == "org.freedesktop.systemd1.Unit" {
					unitPath = signal.Path
				}
			case "org.freedesktop.systemd1.Manager.Reloading":
				reloadState = signal.Body[0].(bool)
				if unitFilesChanged && !reloadState {
					c.sendSubStateReload()
					unitFilesChanged = false
					continue
				}
			case "org.freedesktop.systemd1.Manager.UnitFilesChanged":
				unitFilesChanged = true
				continue
			}

			if unitPath == dbus.ObjectPath("") {
				continue
			}
			c.sendSubStateUpdate(unitPath)
		}
	}()
}

type SubStateUpdate struct {
	UnitName  string
	SubState  string
	FileState string
}

// SetSubStateSubscriber writes to updateCh when any unit's substate changes.
// Although this writes to updateCh on every state change, the reported state
// may be more recent than the change that generated it (due to an unavoidable
// race in the systemd dbus interface).  That is, this method provides a good
// way to keep a current view of all units' states, but is not guaranteed to
// show every state transition they go through.  Furthermore, state changes
// will only be written to the channel with non-blocking writes.  If updateCh
// is full, it attempts to write an error to errCh; if errCh is full, the error
// passes silently.
func (c *Conn) SetSubStateSubscriber(updateCh chan<- *SubStateUpdate, errCh chan<- error, reloadCh chan<- bool) {
	c.subscriber.Lock()
	defer c.subscriber.Unlock()
	c.subscriber.updateCh = updateCh
	c.subscriber.errCh = errCh
	c.subscriber.reloadCh = reloadCh
}

// RemoveSubStateSubscriber removes the active subscriber from the systemd events.
func (c *Conn) RemoveSubStateSubscriber() {
	c.subscriber.Lock()
	defer c.subscriber.Unlock()
	c.subscriber.updateCh = nil
	c.subscriber.errCh = nil
}

func (c *Conn) sendSubStateReload() {
	select {
	case c.subscriber.reloadCh <- true:
	default:
	}
}

func (c *Conn) sendSubStateUpdate(path dbus.ObjectPath) {
	c.subscriber.Lock()
	defer c.subscriber.Unlock()
	var unitName string
	var substate string
	var fileState string

	unitProps, err := c.GetUnitPropertiesFromObjectPath(path)
	if err != nil {
		select {
		case c.subscriber.errCh <- err:
		default:
		}
		return
	}

	substate = unitProps["SubState"].(string)
	unitName = unitProps["Id"].(string)
	fileState = c.GetUnitFileState(unitName)

	update := &SubStateUpdate{unitName, substate, fileState}
	select {
	case c.subscriber.updateCh <- update:
	default:
		select {
		case c.subscriber.errCh <- errors.New("update channel full"):
		default:
		}
	}
}

/*
substate = c.GetUnitFileState(unitName)
Only working call if its reloading.
*/
