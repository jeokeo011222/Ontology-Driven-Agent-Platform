package sse

import (
	"context"

	"odap/internal/execution/outbox"
)

// OutboxSink 把 Outbox 事件桥接到进程内实时总线（MVP 的"内存事件总线"实现）。
//
// 仅用于开发/单实例场景：它把已发布的事件扇出为 SSE 事件。生产环境应由
// 第十篇的 Broker 订阅者承担扇出，Outbox 事件不应被 API 进程消费。
type OutboxSink struct {
	Hub *Hub
	// CriticalityFor 依据事件类型决定分级；为 nil 时一律 NORMAL。
	CriticalityFor func(eventType string) Criticality
}

// Publish 实现 outbox.Sink。
func (s OutboxSink) Publish(_ context.Context, ev outbox.Event) error {
	if s.Hub == nil {
		return nil
	}
	crit := Normal
	if s.CriticalityFor != nil {
		crit = s.CriticalityFor(ev.EventType)
	}
	// ID 留空由 Hub 分配单调序号，保证 SSE 的 Last-Event-ID 语义连续。
	s.Hub.Publish(Event{
		Type:        ev.EventType,
		TenantID:    ev.TenantID.String(),
		Criticality: crit,
		Data:        []byte(ev.Payload),
		OccurredAt:  ev.OccurredAt,
	})
	return nil
}

// EventTypeCriticality 是默认的安全敏感事件分级映射（第十七篇 §10.6）。
func EventTypeCriticality(eventType string) Criticality {
	switch eventType {
	case "CancellationRequested", "ApprovalDecided", "ExternalOperationUnknown",
		"ReconciliationFailed", "ManualInterventionRequired":
		return Critical
	case "ReconciliationRequested", "ReconciliationSucceeded", "AgentPublished",
		"OntologyPublished", "PluginEnabled":
		return Important
	default:
		return Normal
	}
}
