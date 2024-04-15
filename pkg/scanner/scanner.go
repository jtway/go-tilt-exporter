package scanner

import (
	"context"
	"log"
	"time"

	"github.com/JuulLabs-OSS/ble"
	"github.com/JuulLabs-OSS/ble/examples/lib/dev"
	"github.com/jtway/go-tilt"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Scanner for Tilt devices
type Scanner struct {
	devices Devices
	d       ble.Device
	logger  *zap.SugaredLogger
}

// Devices stores discovered devices
type Devices map[tilt.Colour]tilt.Tilt

// NewScanner returns a Scanner
func NewScanner(logger *zap.SugaredLogger) *Scanner {
	return &Scanner{
		logger: logger,
	}
}

// Scan finds Tilt devices and times out after a duration
func (s *Scanner) Scan(timeout time.Duration) {

	s.logger.Infof("Scanning for %v", timeout)

	s.devices = make(map[tilt.Colour]tilt.Tilt)
	var err error = nil

	if s.d == nil {
		s.d, err = dev.NewDevice("go-tilt")
		if err != nil {
			s.logger.Errorf("Unable to initialise new device : %s", err)
		}
		ble.SetDefaultDevice(s.d)
	}
	// Modify to use passed in context and cancel function, or rework to manage its own.
	// However, here we need to modify the advHandler to use channels to communicate each
	// tilt as it is received.
	ctx := ble.WithSigHandler(context.WithTimeout(context.Background(), timeout))
	err = ble.Scan(ctx, true, s.advHandler, advFilter)
	if err != nil {
		switch errors.Cause(err) {
		case nil:
		case context.DeadlineExceeded:
			log.Printf("Finished scanning\n")
		case context.Canceled:
			log.Printf("Cancelled\n")
		default:
			log.Fatalf(err.Error())
		}
	}
}

func advFilter(a ble.Advertisement) bool {
	return tilt.IsTilt(a.ManufacturerData())
}

func (s *Scanner) advHandler(a ble.Advertisement) {

	// create iBeacon
	b, err := tilt.NewIBeacon(a.ManufacturerData())
	if err != nil {
		log.Println(err)
		return
	}

	// create Tilt from iBeacon
	t, err := tilt.NewTilt(b)
	if err != nil {
		log.Println(err)
		return
	}

	s.HandleTilt(t)
}

// HandleTilt adds a discovered Tilt to a map
func (s *Scanner) HandleTilt(t tilt.Tilt) {
	s.devices[t.Colour()] = t
}

// Tilts contains the found devices
func (s *Scanner) Tilts() Devices {
	return s.devices
}
