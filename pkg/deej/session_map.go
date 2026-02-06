package deej

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/omriharel/deej/pkg/deej/util"
	"github.com/thoas/go-funk"
	"go.uber.org/zap"
)

type sessionMap struct {
	deej   *Deej
	logger *zap.SugaredLogger

	m    map[string][]Session
	lock sync.Locker

	sessionFinder SessionFinder

	lastSessionRefresh time.Time
	unmappedSessions   []Session
	lastSliderValues   map[int]float32 // Stores last slider values for auto-sync
}

const (
	masterSessionName = "master" // master device volume
	systemSessionName = "system" // system sounds volume
	inputSessionName  = "mic"    // microphone input level

	// some targets need to be transformed before their correct audio sessions can be accessed.
	// this prefix identifies those targets to ensure they don't contradict with another similarly-named process
	specialTargetTransformPrefix = "deej."

	// targets the currently active window (Windows-only, experimental)
	specialTargetCurrentWindow = "current"

	// targets all currently unmapped sessions (experimental)
	specialTargetAllUnmapped = "unmapped"

	// this threshold constant assumes that re-acquiring all sessions is a kind of expensive operation,
	// and needs to be limited in some manner. this value was previously user-configurable through a config
	// key "process_refresh_frequency", but exposing this type of implementation detail seems wrong now
	minTimeBetweenSessionRefreshes = time.Millisecond * 500

	// determines whether the map should be refreshed when a slider moves.
	// this is a bit greedy but allows us to ensure sessions are always re-acquired, which is
	// especially important for process groups (because you can have one ongoing session
	// always preventing lookup of other processes bound to its slider, which forces the user
	// to manually refresh sessions). a cleaner way to do this down the line is by registering to notifications
	// whenever a new session is added, but that's too hard to justify for how easy this solution is
	maxTimeBetweenSessionRefreshes = time.Second * 45
)

// this matches friendly device names (on Windows), e.g. "Headphones (Realtek Audio)"
var deviceSessionKeyPattern = regexp.MustCompile(`^.+ \(.+\)$`)

func newSessionMap(deej *Deej, logger *zap.SugaredLogger, sessionFinder SessionFinder) (*sessionMap, error) {
	logger = logger.Named("sessions")

	m := &sessionMap{
		deej:             deej,
		logger:           logger,
		m:                make(map[string][]Session),
		lock:             &sync.Mutex{},
		sessionFinder:    sessionFinder,
		lastSliderValues: make(map[int]float32),
	}

	logger.Debug("Created session map instance")

	return m, nil
}

func (m *sessionMap) initialize() error {
	m.refreshSessions(true)
	if len(m.m) == 0 {
		m.logger.Warn("No sessions found during initialization")
	}

	m.setupOnConfigReload()
	m.setupOnSliderMove()
	m.setupSessionWatchdog()

	return nil
}

func (m *sessionMap) release() error {
	if err := m.sessionFinder.Release(); err != nil {
		m.logger.Warnw("Failed to release session finder during session map release", "error", err)
		return fmt.Errorf("release session finder during release: %w", err)
	}

	return nil
}

func (m *sessionMap) setupOnConfigReload() {
	configReloadedChannel := m.deej.config.SubscribeToChanges()

	go func() {
		for {
			select {
			case <-configReloadedChannel:
				m.logger.Info("Detected config reload, attempting to re-acquire all audio sessions")
				m.refreshSessions(false)
			}
		}
	}()
}

// setupSessionWatchdog starts a background goroutine that continuously refreshes sessions
// and applies saved slider values. This ensures new audio streams get the correct volume
// immediately, similar to the volume_watchdog in the Python deej implementation.
func (m *sessionMap) setupSessionWatchdog() {
	go func() {
		ticker := time.NewTicker(minTimeBetweenSessionRefreshes)
		defer ticker.Stop()

		for range ticker.C {
			// Only refresh if we have slider values to apply
			if len(m.lastSliderValues) > 0 {
				m.refreshSessions(false)
			}
		}
	}()

	m.logger.Debug("Started session watchdog (500ms refresh)")
}

func (m *sessionMap) setupOnSliderMove() {
	sliderEventsChannel := m.deej.serial.SubscribeToSliderMoveEvents()

	go func() {
		for {
			select {
			case event := <-sliderEventsChannel:
				// 1. Initial event
				// We use a map to deduplicate events by slider ID, ensuring we only process
				// the latest value for each slider in this batch
				processingQueue := make(map[int]SliderMoveEvent)
				processingQueue[event.SliderID] = event

				// 2. Drain channel (non-blocking)
				// If the producer (Arduino) is faster than the consumer (Windows API),
				// this loop will catch up by discarding intermediate values
			drainLoop:
				for {
					select {
					case nextEvent := <-sliderEventsChannel:
						// Overwrite with newer value for this slider
						processingQueue[nextEvent.SliderID] = nextEvent
					default:
						// Channel empty, stop draining and process what we have
						break drainLoop
					}
				}

				// 3. Process only the winners
				for _, finalEvent := range processingQueue {
					m.handleSliderMoveEvent(finalEvent)
				}
			}
		}
	}()
}

// performance: explain why force == true at every such use to avoid unintended forced refresh spams
// performance: explain why force == true at every such use to avoid unintended forced refresh spams
func (m *sessionMap) refreshSessions(force bool) {

	// make sure enough time passed since the last refresh, unless force is true
	if !force && m.lastSessionRefresh.Add(minTimeBetweenSessionRefreshes).After(time.Now()) {
		return
	}

	// 1. Get ALL sessions first
	sessions, err := m.sessionFinder.GetAllSessions()
	if err != nil {
		m.logger.Warnw("Failed to get sessions from session finder", "error", err)
		return
	}

	// Snapshot of existing keys to detect new sessions without locking in loop
	m.lock.Lock()
	existingKeys := make(map[string]bool, len(m.m))
	for k := range m.m {
		existingKeys[k] = true
	}
	m.lock.Unlock()

	// 2. Build NEW map locally
	newMap := make(map[string][]Session)
	var newUnmappedSessions []Session

	for _, session := range sessions {
		key := session.Key()
		newMap[key] = append(newMap[key], session)

		if !m.sessionMapped(session) {
			newUnmappedSessions = append(newUnmappedSessions, session)

			// If this is a NEW unmapped session (not in old map), default to 20%
			// This prevents unknown apps from blasting at 100% volume
			if !existingKeys[key] {
				m.logger.Debugw("Setting default volume for new unmapped session", "session", key)
				session.SetVolume(0.20)
			}
		}
	}

	// 3. Swap maps atomically
	m.lock.Lock()
	m.m = newMap
	m.unmappedSessions = newUnmappedSessions
	m.lastSessionRefresh = time.Now()
	m.lock.Unlock()

	// 4. Apply values
	m.applyCurrentSliderValues()

	m.logger.Debug("Refreshed sessions successfully (atomic update)")
}

// returns true if a session is not currently mapped to any slider, false otherwise
// special sessions (master, system, mic) and device-specific sessions always count as mapped,
// even when absent from the config. this makes sense for every current feature that uses "unmapped sessions"
func (m *sessionMap) sessionMapped(session Session) bool {

	// count master/system/mic as mapped
	if funk.ContainsString([]string{masterSessionName, systemSessionName, inputSessionName}, session.Key()) {
		return true
	}

	// count device sessions as mapped
	if deviceSessionKeyPattern.MatchString(session.Key()) {
		return true
	}

	matchFound := false

	// look through the actual mappings
	m.deej.config.SliderMapping.iterate(func(sliderIdx int, targets []string) {
		for _, target := range targets {

			// ignore special transforms
			if m.targetHasSpecialTransform(target) {
				continue
			}

			// safe to assume this has a single element because we made sure there's no special transform
			target = m.resolveTarget(target)[0]

			if target == session.Key() {
				matchFound = true
				return
			}
		}
	})

	return matchFound
}

func (m *sessionMap) handleSliderMoveEvent(event SliderMoveEvent) {
	// Store the slider value for auto-sync with new sessions
	m.lastSliderValues[event.SliderID] = event.PercentValue

	// first of all, ensure our session map isn't moldy
	if m.lastSessionRefresh.Add(maxTimeBetweenSessionRefreshes).Before(time.Now()) {
		m.logger.Debug("Stale session map detected on slider move, refreshing")
		m.refreshSessions(true)
	}

	// get the targets mapped to this slider from the config
	targets, ok := m.deej.config.SliderMapping.get(event.SliderID)

	// if slider not found in config, silently ignore
	if !ok {
		return
	}

	targetFound := false
	adjustmentFailed := false

	// for each possible target for this slider...
	for _, target := range targets {

		// resolve the target name by cleaning it up and applying any special transformations.
		// depending on the transformation applied, this can result in more than one target name
		resolvedTargets := m.resolveTarget(target)

		// for each resolved target...
		for _, resolvedTarget := range resolvedTargets {

			// check the map for matching sessions
			sessions, ok := m.get(resolvedTarget)

			// no sessions matching this target - move on
			if !ok {
				continue
			}

			targetFound = true

			// iterate all matching sessions and adjust the volume of each one
			for _, session := range sessions {
				if session.GetVolume() != event.PercentValue {
					if err := session.SetVolume(event.PercentValue); err != nil {
						m.logger.Warnw("Failed to set target session volume", "error", err)
						adjustmentFailed = true
					}
				}
			}
		}
	}

	// if we still haven't found a target or the volume adjustment failed, maybe look for the target again.
	// processes could've opened since the last time this slider moved.
	// if they haven't, the cooldown will take care to not spam it up
	if !targetFound {
		m.refreshSessions(false)
	} else if adjustmentFailed {

		// performance: the reason that forcing a refresh here is okay is that we'll only get here
		// when a session's SetVolume call errored, such as in the case of a stale master session
		// (or another, more catastrophic failure happens)
		m.refreshSessions(true)
	}
}

func (m *sessionMap) targetHasSpecialTransform(target string) bool {
	return strings.HasPrefix(target, specialTargetTransformPrefix)
}

func (m *sessionMap) resolveTarget(target string) []string {

	// start by ignoring the case
	target = strings.ToLower(target)

	// look for any special targets first, by examining the prefix
	if m.targetHasSpecialTransform(target) {
		return m.applyTargetTransform(strings.TrimPrefix(target, specialTargetTransformPrefix))
	}

	return []string{target}
}

func (m *sessionMap) applyTargetTransform(specialTargetName string) []string {

	// select the transformation based on its name
	switch specialTargetName {

	// get current active window
	case specialTargetCurrentWindow:
		currentWindowProcessNames, err := util.GetCurrentWindowProcessNames()

		// silently ignore errors here, as this is on deej's "hot path" (and it could just mean the user's running linux)
		if err != nil {
			return nil
		}

		// we could have gotten a non-lowercase names from that, so let's ensure we return ones that are lowercase
		for targetIdx, target := range currentWindowProcessNames {
			currentWindowProcessNames[targetIdx] = strings.ToLower(target)
		}

		// remove dupes
		return funk.UniqString(currentWindowProcessNames)

	// get currently unmapped sessions
	case specialTargetAllUnmapped:
		targetKeys := make([]string, len(m.unmappedSessions))
		for sessionIdx, session := range m.unmappedSessions {
			targetKeys[sessionIdx] = session.Key()
		}

		return targetKeys
	}

	return nil
}

// applyCurrentSliderValues applies saved slider values to all mapped sessions.
// This ensures new sessions get the correct volume immediately after being discovered.
func (m *sessionMap) applyCurrentSliderValues() {
	m.deej.config.SliderMapping.iterate(func(sliderIdx int, targets []string) {
		value, ok := m.lastSliderValues[sliderIdx]
		if !ok {
			return // No saved value for this slider yet
		}

		for _, target := range targets {
			resolvedTargets := m.resolveTarget(target)
			for _, resolvedTarget := range resolvedTargets {
				if sessions, ok := m.get(resolvedTarget); ok {
					for _, session := range sessions {
						// Check if volume difference is significant (more than 1%)
						// This prevents spamming PulseAudio with tiny adjustments due to float precision
						diff := float64(session.GetVolume() - value)
						if math.Abs(diff) > 0.01 {
							m.logger.Debugw("Applying saved slider value to session",
								"session", session.Key(),
								"value", value,
								"current", session.GetVolume())
							session.SetVolume(value)
						}
					}
				}
			}
		}
	})
}

func (m *sessionMap) add(value Session) {
	m.lock.Lock()
	defer m.lock.Unlock()

	key := value.Key()

	existing, ok := m.m[key]
	if !ok {
		m.m[key] = []Session{value}
	} else {
		m.m[key] = append(existing, value)
	}
}

func (m *sessionMap) get(key string) ([]Session, bool) {
	m.lock.Lock()
	defer m.lock.Unlock()

	value, ok := m.m[key]
	return value, ok
}

func (m *sessionMap) clear() {
	m.lock.Lock()
	defer m.lock.Unlock()

	// We only clear the map structure, individual sessions don't need explicit release for PulseAudio
	m.m = make(map[string][]Session)
	m.logger.Debug("Session map cleared")
}

func (m *sessionMap) String() string {
	m.lock.Lock()
	defer m.lock.Unlock()

	sessionCount := 0

	for _, value := range m.m {
		sessionCount += len(value)
	}

	return fmt.Sprintf("<%d audio sessions>", sessionCount)
}
