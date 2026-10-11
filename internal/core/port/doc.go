// Package port defines the interfaces between the core and the outside world.
// Ports live in the core because the core is their consumer: incoming ports
// are what primary adapters call, outgoing ports are what the core calls out
// to. Adapters implement the outgoing ports; the composition root wires them.
package port
