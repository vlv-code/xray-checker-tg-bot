package xray

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"

	"xray-checker/logger"

	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
)

type filteredLogHandler struct{}

func (h *filteredLogHandler) Handle(msg log.Message) {
	msgStr := msg.String()
	if strings.Contains(msgStr, "deprecated") {
		return
	}
	if strings.HasPrefix(msgStr, "[Warning]") {
		return
	}
	logger.Debug("xray: %s", msgStr)
}

func init() {
	log.RegisterHandler(&filteredLogHandler{})
}

type Runner struct {
	// mu guards instance: Start (write) and Stop (read/clear) can race when
	// the deferred shutdown Stop overlaps a scheduler-driven restart in
	// updateConfiguration. Callers' external locks (e.g. checkRunnerMu in
	// main.go) are never acquired inside Runner methods, so taking mu here
	// cannot invert lock order.
	mu         sync.Mutex
	instance   *core.Instance
	configFile string
}

func NewRunner(configFile string) *Runner {
	return &Runner{
		configFile: configFile,
	}
}

func (r *Runner) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	configBytes, err := os.ReadFile(r.configFile)
	if err != nil {
		return fmt.Errorf("error reading config file: %v", err)
	}

	xrayConfig, err := serial.DecodeJSONConfig(bytes.NewReader(configBytes))
	if err != nil {
		return fmt.Errorf("error decoding config: %v", err)
	}

	coreConfig, err := xrayConfig.Build()
	if err != nil {
		return fmt.Errorf("error building config: %v", err)
	}

	instance, err := core.New(coreConfig)
	if err != nil {
		return fmt.Errorf("error creating Xray instance: %v", err)
	}

	if err := instance.Start(); err != nil {
		// core.New allocates listeners/features that only Close() releases;
		// a failed Start must not strand them (updateConfiguration retries
		// Start on the next subscription reload).
		instance.Close()
		return fmt.Errorf("error starting Xray: %v", err)
	}

	r.instance = instance
	logger.Debug("Xray instance started")

	return nil
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.instance != nil {
		err := r.instance.Close()
		r.instance = nil
		if err != nil {
			return fmt.Errorf("error stopping Xray: %v", err)
		}
		logger.Debug("Xray instance stopped")
	}
	return nil
}
