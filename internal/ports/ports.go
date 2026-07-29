package ports

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"

	"github.com/nicolaeser/HermesManager/internal/config"
)

const searchWindow = 2000

func Select(root, bindAddress string, requestedDashboard, requestedAPI int) (int, int, error) {
	seed := int(config.FolderHash(root))
	dashboardStart := 9119 + seed%1000
	apiStart := 12000 + seed%1000

	dashboard, err := selectOne(bindAddress, requestedDashboard, dashboardStart, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("dashboard port: %w", err)
	}
	api, err := selectOne(bindAddress, requestedAPI, apiStart, map[int]bool{dashboard: true})
	if err != nil {
		return 0, 0, fmt.Errorf("API port: %w", err)
	}
	return dashboard, api, nil
}

func selectOne(bindAddress string, requested, start int, excluded map[int]bool) (int, error) {
	if requested != 0 {
		if requested < 1 || requested > 65535 {
			return 0, fmt.Errorf("%d is outside 1-65535", requested)
		}
		if excluded[requested] {
			return 0, fmt.Errorf("%d is already selected for this instance", requested)
		}
		if !available(bindAddress, requested) {
			return 0, fmt.Errorf("%d is already in use on %s", requested, bindAddress)
		}
		return requested, nil
	}
	for offset := 0; offset < searchWindow; offset++ {
		candidate := start + offset
		if candidate > 65535 || excluded[candidate] {
			continue
		}
		if available(bindAddress, candidate) {
			return candidate, nil
		}
	}
	return 0, fmt.Errorf("no available localhost port found near %d", start)
}

func available(bindAddress string, port int) bool {
	listener, err := net.Listen("tcp4", net.JoinHostPort(bindAddress, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			return true
		}
		return false
	}
	return listener.Close() == nil
}
