package app

import (
	"errors"
	"sync"
	"time"

	"podmanager/internal/iap"
	"podmanager/internal/ipod"
)

// ErrNoAccessoryPort means the iPod's USB accessory interface wasn't found.
var ErrNoAccessoryPort = errors.New("this iPod didn't show its accessory interface over USB")

// BatteryView is the iPod's battery as shown in the UI.
type BatteryView struct {
	Available bool   `json:"available"`
	Percent   int    `json:"percent"` // 0-100, or -1 when unknown
	Charging  bool   `json:"charging"`
	Charged   bool   `json:"charged"`
	External  bool   `json:"external"` // plugged into power
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"` // why it's unavailable
	Checked   int64  `json:"checked"`          // unix ms
}

type batteryCache struct {
	mu   sync.Mutex
	root string
	at   time.Time
	view BatteryView
}

const batteryMaxAge = 15 * time.Second

// Battery reads the battery state of the iPod at root over its USB
// accessory interface. Results are cached briefly, since hovering the
// battery icon and the periodic refresh can ask in quick succession.
func (a *App) Battery(root string) (*BatteryView, error) {
	if root == "" {
		return &BatteryView{Percent: -1, Reason: "No iPod is open."}, nil
	}
	c := &a.batt
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == root && time.Since(c.at) < batteryMaxAge {
		v := c.view
		return &v, nil
	}
	v := BatteryView{Percent: -1, Checked: time.Now().UnixMilli()}
	b, err := a.plat.Battery(ipod.FirewireGUID(root))
	switch {
	case err == nil:
		v.Available = true
		v.Percent = b.Percent()
		v.Charging = b.Charging()
		v.Charged = b.Charged()
		v.External = b.OnExternalPower()
		v.State = b.Describe()
	case errors.Is(err, ErrNoAccessoryPort):
		v.Reason = "This iPod doesn't report its battery over USB."
	case errors.Is(err, iap.ErrTimeout):
		v.Reason = "The iPod didn't answer the battery request."
	default:
		v.Reason = "Couldn't read the battery: " + err.Error()
	}
	c.root, c.at, c.view = root, time.Now(), v
	return &v, nil
}
