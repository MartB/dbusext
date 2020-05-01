package dbusext

import "github.com/godbus/dbus"

type Property struct {
	Name  string
	Value dbus.Variant
}

type PropertyCollection struct {
	Name       string
	Properties []Property
}
