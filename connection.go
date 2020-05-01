package dbusext

import (
	"os"
	"strconv"
	"sync"

	"github.com/godbus/dbus"
)

// Conn is a connection to systemd's dbus endpoint.
type Conn struct {
	// sysconn/sysobj are only used to call dbus methods
	sysconn *dbus.Conn
	sysobj  dbus.BusObject

	// sigconn/sigobj are only used to receive dbus signals
	sigconn *dbus.Conn
	sigobj  dbus.BusObject

	jobListener struct {
		jobs map[dbus.ObjectPath]chan<- string
		sync.Mutex
	}

	subscriber struct {
		updateCh chan<- *SubStateUpdate
		errCh    chan<- error
		reloadCh chan<- bool
		sync.Mutex
	}
}

func (c *Conn) Raw() *dbus.Conn {
	return c.sysconn
}

// NewSystemConnection establishes a connection to the system bus and authenticates.
// Callers should call Close() when done with the connection
func NewSystemConnection() (*Conn, error) {
	return NewConnection(func() (*dbus.Conn, error) {
		return dbusAuthHelloConnection(dbus.SystemBusPrivate)
	})
}

// NewUserConnection establishes a connection to the session bus and
// authenticates. This can be used to connect to systemd user instances.
// Callers should call Close() when done with the connection.
func NewUserConnection() (*Conn, error) {
	return NewConnection(func() (*dbus.Conn, error) {
		return dbusAuthHelloConnection(dbus.SessionBusPrivate)
	})
}

// NewSystemdConnection establishes a private, direct connection to systemd.
// This can be used for communicating with systemd without a dbus daemon.
// Callers should call Close() when done with the connection.
func NewSystemdConnection() (*Conn, error) {
	return NewConnection(func() (*dbus.Conn, error) {
		// We skip Hello when talking directly to systemd.
		return dbusAuthConnection(func() (*dbus.Conn, error) {
			return dbus.Dial("unix:path=/run/systemd/private")
		})
	})
}

// Close closes an established connection
func (c *Conn) Close() {
	c.sysconn.Close()
	c.sigconn.Close()
}

// NewConnection establishes a connection to a bus using a caller-supplied function.
// This allows connecting to remote buses through a user-supplied mechanism.
// The supplied function may be called multiple times, and should return independent connections.
// The returned connection must be fully initialised: the org.freedesktop.DBus.Hello call must have succeeded,
// and any authentication should be handled by the function.
func NewConnection(dialBus func() (*dbus.Conn, error)) (*Conn, error) {
	sysconn, err := dialBus()
	if err != nil {
		return nil, err
	}

	sigconn, err := dialBus()
	if err != nil {
		sysconn.Close()
		return nil, err
	}

	c := &Conn{
		sysconn: sysconn,
		sysobj:  systemdObject(sysconn),
		sigconn: sigconn,
		sigobj:  systemdObject(sigconn),
	}

	c.jobListener.jobs = make(map[dbus.ObjectPath]chan<- string)

	// Setup the listeners on jobs so that we can get completions
	c.sigconn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0,
		"type='signal', interface='org.freedesktop.systemd1.Manager', member='JobRemoved'")
	c.dispatch()
	return c, nil
}

// GetManagerProperty returns the value of a property on the org.freedesktop.systemd1.Manager
// interface. The value is returned in its string representation, as defined at
// https://developer.gnome.org/glib/unstable/gvariant-text.html
func (c *Conn) GetManagerProperty(prop string) (string, error) {
	variant, err := c.sysobj.GetProperty("org.freedesktop.systemd1.Manager." + prop)
	if err != nil {
		return "", err
	}
	return variant.String(), nil
}

func dbusAuthConnection(createBus func() (*dbus.Conn, error)) (*dbus.Conn, error) {
	conn, err := createBus()
	if err != nil {
		return nil, err
	}

	// Only use EXTERNAL method, and hardcode the uid (not username)
	// to avoid a username lookup (which requires a dynamically linked
	// libc)
	methods := []dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}

	err = conn.Auth(methods)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

func dbusAuthHelloConnection(createBus func() (*dbus.Conn, error)) (*dbus.Conn, error) {
	conn, err := dbusAuthConnection(createBus)
	if err != nil {
		return nil, err
	}

	if err = conn.Hello(); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

func systemdObject(conn *dbus.Conn) dbus.BusObject {
	return conn.Object("org.freedesktop.systemd1", dbus.ObjectPath("/org/freedesktop/systemd1"))
}
