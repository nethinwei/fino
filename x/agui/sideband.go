package agui

// This file holds emitter constructors for AG-UI side-band events: state,
// activity, raw, and custom. These events do not originate from a fino run —
// fino has no state or activity concept — so the application layer emits them
// alongside a run's lifecycle events. The constructors exist so callers set the
// discriminating Type field correctly rather than building literals by hand.

// NewStateSnapshotEvent builds a STATE_SNAPSHOT carrying the complete state.
func NewStateSnapshotEvent(snapshot any) StateSnapshotEvent {
	return StateSnapshotEvent{
		BaseEvent: BaseEvent{Type: EventStateSnapshot},
		Snapshot:  snapshot,
	}
}

// NewStateDeltaEvent builds a STATE_DELTA from RFC 6902 JSON Patch operations.
// A zero-argument call produces an empty patch array (serialized as []), not null.
func NewStateDeltaEvent(delta ...JSONPatchOp) StateDeltaEvent {
	if delta == nil {
		delta = []JSONPatchOp{}
	}
	return StateDeltaEvent{
		BaseEvent: BaseEvent{Type: EventStateDelta},
		Delta:     delta,
	}
}

// NewActivitySnapshotEvent builds an ACTIVITY_SNAPSHOT for one activity message.
func NewActivitySnapshotEvent(messageID, activityType string, content any) ActivitySnapshotEvent {
	return ActivitySnapshotEvent{
		BaseEvent:    BaseEvent{Type: EventActivitySnapshot},
		MessageID:    messageID,
		ActivityType: activityType,
		Content:      content,
	}
}

// NewActivityDeltaEvent builds an ACTIVITY_DELTA from RFC 6902 JSON Patch
// operations applied to an existing activity message.
// A zero-argument patch call produces an empty patch array (serialized as []), not null.
func NewActivityDeltaEvent(messageID, activityType string, patch ...JSONPatchOp) ActivityDeltaEvent {
	if patch == nil {
		patch = []JSONPatchOp{}
	}
	return ActivityDeltaEvent{
		BaseEvent:    BaseEvent{Type: EventActivityDelta},
		MessageID:    messageID,
		ActivityType: activityType,
		Patch:        patch,
	}
}

// NewRawEvent builds a RAW event carrying an implementation-specific payload.
// source, if non-nil, identifies the originating component.
func NewRawEvent(event any, source *string) RawEvent {
	return RawEvent{
		BaseEvent: BaseEvent{Type: EventRaw},
		Event:     event,
		Source:    source,
	}
}

// NewCustomEvent builds a CUSTOM event with an application-defined name and value.
func NewCustomEvent(name string, value any) CustomEvent {
	return CustomEvent{
		BaseEvent: BaseEvent{Type: EventCustom},
		Name:      name,
		Value:     value,
	}
}
