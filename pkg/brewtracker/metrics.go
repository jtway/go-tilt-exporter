package brewtracker

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const Namespace = "brewtracker"

type metrics struct {
	beerReading                 *prometheus.CounterVec
	beerMeasuredOriginalGravity *prometheus.GaugeVec
	beerEstimatedFinalGravity   *prometheus.GaugeVec
	beerEstimatedIbu            *prometheus.GaugeVec
	beerEstimatedSrm            *prometheus.GaugeVec
	beerGravity                 *prometheus.GaugeVec
	beerTemperatureF            *prometheus.GaugeVec
	beerTemperatureC            *prometheus.GaugeVec
}

func NewMetrics() *metrics {
	m := &metrics{
		beerReading: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "tilt",
			Name:      "readings_taken_total",
			Help:      "total number of beer readings taken",
		},
			[]string{"color"},
		),
		beerMeasuredOriginalGravity: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "measured_og",
			Help:      "Measured original gravity",
		},
			[]string{"id", "name"},
		),
		beerEstimatedFinalGravity: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "estimated_fg",
			Help:      "Brewfather estimated final gravity",
		},
			[]string{"id", "name"},
		),
		beerEstimatedIbu: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "estimated_ibu",
			Help:      "Brewfather estimated IBU",
		},
			[]string{"id", "name"},
		),
		beerEstimatedSrm: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "estimated_srm",
			Help:      "Estimated SRM",
		},
			[]string{"id", "name"},
		),
		beerGravity: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "gravity_reading",
			Help:      "latest specfic gravity reading",
		},
			[]string{"id", "name", "tilt_color"},
		),
		beerTemperatureF: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "temperature_reading_f",
			Help:      "latest temperature reading",
		},
			[]string{"id", "name", "tilt_color"},
		),
		beerTemperatureC: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "temperature_reading_c",
			Help:      "latest temperature reading",
		},
			[]string{"id", "name", "tilt_color"},
		),
	}
	return m
}
