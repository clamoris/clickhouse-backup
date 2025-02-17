package metrics

import (
	"fmt"
	apexLog "github.com/apex/log"
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

type APIMetricsInterface interface {
	Start(command string, startTime time.Time)
	Finish(command string, startTime time.Time)
	Success(command string)
	Failure(command string)
	ExecuteWithMetrics(command string, errCounter int, f func() error) (error, int)
}

type APIMetrics struct {
	SuccessfulCounter map[string]prometheus.Counter
	FailedCounter     map[string]prometheus.Counter
	LastStart         map[string]prometheus.Gauge
	LastFinish        map[string]prometheus.Gauge
	LastDuration      map[string]prometheus.Gauge
	LastStatus        map[string]prometheus.Gauge

	LastBackupSizeLocal         prometheus.Gauge
	LastBackupSizeRemote        prometheus.Gauge
	NumberBackupsRemote         prometheus.Gauge
	NumberBackupsRemoteBroken   prometheus.Gauge
	NumberBackupsLocal          prometheus.Gauge
	NumberBackupsRemoteExpected prometheus.Gauge
	NumberBackupsLocalExpected  prometheus.Gauge
	log                         *apexLog.Entry
}

func NewAPIMetrics() *APIMetrics {
	metrics := &APIMetrics{
		log: apexLog.WithField("logger", "metrics"),
	}
	return metrics
}

// RegisterMetrics resister prometheus metrics and define allowed measured commands list
func (m *APIMetrics) RegisterMetrics() {
	commandList := []string{"create", "upload", "download", "restore", "create_remote", "restore_remote", "delete"}
	successfulCounter := map[string]prometheus.Counter{}
	failedCounter := map[string]prometheus.Counter{}
	lastStart := map[string]prometheus.Gauge{}
	lastFinish := map[string]prometheus.Gauge{}
	lastDuration := map[string]prometheus.Gauge{}
	lastStatus := map[string]prometheus.Gauge{}

	for _, command := range commandList {
		successfulCounter[command] = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "clickhouse_backup",
			Name:      fmt.Sprintf("successful_%ss", command),
			Help:      fmt.Sprintf("Counter of successful %ss backup", command),
		})
		failedCounter[command] = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "clickhouse_backup",
			Name:      fmt.Sprintf("failed_%ss", command),
			Help:      fmt.Sprintf("Counter of failed %ss backup", command),
		})
		lastStart[command] = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "clickhouse_backup",
			Name:      fmt.Sprintf("last_%s_start", command),
			Help:      fmt.Sprintf("Last backup %s start timestamp", command),
		})
		lastFinish[command] = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "clickhouse_backup",
			Name:      fmt.Sprintf("last_%s_finish", command),
			Help:      fmt.Sprintf("Last backup %s finish timestamp", command),
		})
		lastDuration[command] = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "clickhouse_backup",
			Name:      fmt.Sprintf("last_%s_duration", command),
			Help:      fmt.Sprintf("Backup %s duration in nanoseconds", command),
		})
		lastStatus[command] = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "clickhouse_backup",
			Name:      fmt.Sprintf("last_%s_status", command),
			Help:      fmt.Sprintf("Last backup %s status: 0=failed, 1=success, 2=unknown", command),
		})
	}

	m.SuccessfulCounter = successfulCounter
	m.FailedCounter = failedCounter
	m.LastStart = lastStart
	m.LastFinish = lastFinish
	m.LastDuration = lastDuration
	m.LastStatus = lastStatus

	m.LastBackupSizeLocal = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "clickhouse_backup",
		Name:      "last_backup_size_local",
		Help:      "Last local backup size in bytes",
	})
	m.LastBackupSizeRemote = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "clickhouse_backup",
		Name:      "last_backup_size_remote",
		Help:      "Last remote backup size in bytes",
	})

	m.NumberBackupsRemote = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "clickhouse_backup",
		Name:      "number_backups_remote",
		Help:      "Number of stored remote backups",
	})

	m.NumberBackupsRemoteBroken = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "clickhouse_backup",
		Name:      "number_backups_remote_broken",
		Help:      "Number of broken remote backups",
	})

	m.NumberBackupsRemoteExpected = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "clickhouse_backup",
		Name:      "number_backups_remote_expected",
		Help:      "How many backups expected on remote storage",
	})

	m.NumberBackupsLocal = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "clickhouse_backup",
		Name:      "number_backups_local",
		Help:      "Number of stored local backups",
	})

	m.NumberBackupsLocalExpected = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "clickhouse_backup",
		Name:      "number_backups_local_expected",
		Help:      "How many backups expected on local storage",
	})

	for _, command := range commandList {
		prometheus.MustRegister(
			m.SuccessfulCounter[command],
			m.FailedCounter[command],
			m.LastStart[command],
			m.LastFinish[command],
			m.LastDuration[command],
			m.LastStatus[command],
		)
	}

	prometheus.MustRegister(
		m.LastBackupSizeLocal,
		m.LastBackupSizeRemote,
		m.NumberBackupsRemote,
		m.NumberBackupsLocal,
		m.NumberBackupsRemoteExpected,
		m.NumberBackupsLocalExpected,
	)

	for _, command := range commandList {
		m.LastStatus[command].Set(2) // 0=failed, 1=success, 2=unknown
	}
}

func (m *APIMetrics) Start(command string, startTime time.Time) {
	if _, exists := m.LastStart[command]; exists {
		m.LastStart[command].Set(float64(startTime.Unix()))
	} else {
		m.log.Warnf("%s not found in m", command)
	}
}
func (m *APIMetrics) Finish(command string, startTime time.Time) {
	if _, exists := m.LastStart[command]; exists {
		m.LastDuration[command].Set(float64(time.Since(startTime).Nanoseconds()))
		m.LastFinish[command].Set(float64(time.Now().Unix()))
	} else {
		m.log.Warnf("%s not found in m", command)
	}
}
func (m *APIMetrics) Success(command string) {
	if _, exists := m.LastStart[command]; exists {
	} else {
		m.log.Warnf("%s not found in m", command)
	}
}

func (m *APIMetrics) Failure(command string) {
	if _, exists := m.LastStart[command]; exists {
	} else {
		m.log.Warnf("%s not found in m", command)
	}
}

func (m *APIMetrics) ExecuteWithMetrics(command string, errCounter int, f func() error) (error, int) {
	startTime := time.Now()
	m.Start(command, startTime)
	err := f()
	m.Finish(command, startTime)
	if err != nil {
		m.log.Errorf("metrics.ExecuteWithMetrics(%s) return error: %v", command, err)
		errCounter += 1
		m.Failure(command)
	} else {
		errCounter = 0
		m.Success(command)
	}
	return err, errCounter
}
