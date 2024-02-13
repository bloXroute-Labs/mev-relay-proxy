package api

import (
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"
	"time"
)

type LogMetric struct {
	fields     []zap.Field
	attributes []attribute.KeyValue
}

func NewLogMetric(fields []zap.Field, attributes []attribute.KeyValue) *LogMetric {
	return &LogMetric{
		fields:     fields,
		attributes: attributes,
	}
}
func (l *LogMetric) String(k string, v string) {
	l.fields = append(l.fields, zap.String(k, v))
	l.attributes = append(l.attributes, attribute.String(k, v))
}

func (l *LogMetric) Int64(k string, v int64) {
	l.fields = append(l.fields, zap.Int64(k, v))
	l.attributes = append(l.attributes, attribute.Int64(k, v))
}

func (l *LogMetric) Time(k string, v time.Time) {
	l.fields = append(l.fields, zap.Time(k, v))
	l.attributes = append(l.attributes, attribute.Int64(k, v.Unix()))
}

func (l *LogMetric) Error(err error) {
	l.fields = append(l.fields, zap.Error(err))
	l.attributes = append(l.attributes, attribute.String("err", err.Error()))
}
func (l *LogMetric) Fields(field ...zap.Field) {
	if field != nil {
		l.fields = append(l.fields, field...)
	}
}
func (l *LogMetric) Attributes(attributes ...attribute.KeyValue) {
	if attributes != nil {
		l.attributes = append(l.attributes, attributes...)
	}
}

func (l *LogMetric) Merge(m *LogMetric) {
	if m != nil && m.fields != nil {
		l.fields = append(l.fields, m.fields...)
	}
	if m != nil && m.attributes != nil {
		l.attributes = append(l.attributes, m.attributes...)
	}
}
