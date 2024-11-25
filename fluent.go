package logrus_fluent

import (
	"fmt"
	"github.com/IBM/fluent-forward-go/fluent/client"
	"github.com/sirupsen/logrus"
)

const (
	// TagName is struct field tag name.
	// Some basic option is allowed in the field tag,
	//
	// type myStruct {
	//     Value1: `fluent:"value_1"`    // change field name.
	//     Value2: `fluent:"-"`          // always omit this field.
	//     Value3: `fluent:",omitempty"` // omit this field when zero-value.
	// }
	TagName = "fluent"
	// TagField is logrus field name used as fluentd tag
	TagField = "tag"
	// MessageField is logrus field name used as message.
	// If missing in the log fields, entry.Message is set to this field.
	MessageField = "message"
)

var defaultLevels = []logrus.Level{
	logrus.PanicLevel,
	logrus.FatalLevel,
	logrus.ErrorLevel,
	logrus.WarnLevel,
	logrus.InfoLevel,
}

// FluentHook is logrus hook for fluentd.
type FluentHook struct {
	// Fluent is actual fluentd logger.
	// If set, this logger is used for logging.
	// otherwise new logger is created every time.
	Fluent *client.Client
	conf   Config

	levels []logrus.Level
	tag    *string

	messageField string
	ignoreFields map[string]struct{}
	filters      map[string]func(interface{}) interface{}
	customizers  []func(entry *logrus.Entry, data logrus.Fields)
}

// New returns initialized logrus hook for fluentd with persistent fluentd logger.
func New(host string, port int) (*FluentHook, error) {
	return NewWithConfig(Config{
		Host:                host,
		Port:                port,
		DefaultMessageField: MessageField,
	})
}

// NewWithConfig returns initialized logrus hook by config setting.
func NewWithConfig(conf Config) (*FluentHook, error) {
	var fd *client.Client
	if !conf.DisableConnectionPool {
		fd = client.New(client.ConnectionOptions{
			Factory: &client.ConnFactory{
				Address: fmt.Sprintf("%s:%d", conf.Host, conf.Port),
			},
		})
		err := fd.Connect()
		if err != nil {
			return nil, err
		}
	}

	hook := &FluentHook{
		Fluent:       fd,
		conf:         conf,
		levels:       conf.LogLevels,
		ignoreFields: make(map[string]struct{}),
		filters:      make(map[string]func(interface{}) interface{}),
	}
	// set default values
	if len(hook.levels) == 0 {
		hook.levels = defaultLevels
	}
	if conf.DefaultTag != "" {
		tag := conf.DefaultTag
		hook.tag = &tag
	}
	if conf.DefaultMessageField != "" {
		hook.messageField = conf.DefaultMessageField
	} else {
		hook.messageField = MessageField
	}

	for k, v := range conf.DefaultIgnoreFields {
		hook.ignoreFields[k] = v
	}
	for k, v := range conf.DefaultFilters {
		hook.filters[k] = v
	}

	return hook, nil
}

// NewHook returns initialized logrus hook for fluentd.
// (** deperecated: use New() or NewWithConfig() **)
func NewHook(host string, port int) *FluentHook {
	hook, _ := NewWithConfig(Config{
		Host:                  host,
		Port:                  port,
		DefaultMessageField:   MessageField,
		DisableConnectionPool: true,
	})
	return hook
}

// Levels returns logging level to fire this hook.
func (hook *FluentHook) Levels() []logrus.Level {
	return hook.levels
}

// SetLevels sets logging level to fire this hook.
func (hook *FluentHook) SetLevels(levels []logrus.Level) {
	hook.levels = levels
}

// Tag returns custom static tag.
func (hook *FluentHook) Tag() string {
	if hook.tag == nil {
		return ""
	}

	return *hook.tag
}

// SetTag sets custom static tag to override tag in the message fields.
func (hook *FluentHook) SetTag(tag string) {
	hook.tag = &tag
}

// SetMessageField sets custom message field.
func (hook *FluentHook) SetMessageField(messageField string) {
	hook.messageField = messageField
}

// AddIgnore adds field name to ignore.
func (hook *FluentHook) AddIgnore(name string) {
	hook.ignoreFields[name] = struct{}{}
}

// AddFilter adds a custom filter function.
func (hook *FluentHook) AddFilter(name string, fn func(interface{}) interface{}) {
	hook.filters[name] = fn
}

// AddCustomizer adds a custom function to modify data.
func (hook *FluentHook) AddCustomizer(fn func(entry *logrus.Entry, data logrus.Fields)) {
	hook.customizers = append(hook.customizers, fn)
}

// Fire is invoked by logrus and sends log to fluentd logger.
func (hook *FluentHook) Fire(entry *logrus.Entry) error {
	var logger *client.Client
	var err error

	switch {
	case hook.Fluent != nil:
		logger = hook.Fluent
	default:
		logger = client.New(client.ConnectionOptions{
			Factory: &client.ConnFactory{
				Address: fmt.Sprintf("%s:%d", hook.conf.Host, hook.conf.Port),
			},
		})
		err := logger.Connect()

		//logger, err = fluent.New(hook.conf.FluentConfig())

		if err != nil {
			return err
		}
		defer logger.Disconnect()
	}

	// Create a map for passing to FluentD
	data := make(logrus.Fields)
	for k, v := range entry.Data {
		if _, ok := hook.ignoreFields[k]; ok {
			continue
		}
		if fn, ok := hook.filters[k]; ok {
			v = fn(v)
		}
		data[k] = v
	}

	setLevelString(entry, data)
	hook.setMessage(entry, data)

	// modify data to your own needs.
	for _, fn := range hook.customizers {
		fn(entry, data)
	}
	tag := hook.getTagAndDel(entry, data)
	fluentData := ConvertToValue(data, TagName)
	err = logger.SendMessage(tag, fluentData)
	return err
}

// getTagAndDel extracts tag data from log entry and custom log fields.
// 1. if tag is set in the hook, use it.
// 2. if tag is set in custom fields, use it.
// 3. if cannot find tag data, use entry.Message as tag.
func (hook *FluentHook) getTagAndDel(entry *logrus.Entry, data logrus.Fields) string {
	// use static tag from
	if hook.tag != nil {
		return *hook.tag
	}

	tagField, ok := data[TagField]
	if !ok {
		return entry.Message
	}

	tag, ok := tagField.(string)
	if !ok {
		return entry.Message
	}

	// remove tag from data fields
	delete(data, TagField)
	return tag
}

func (hook *FluentHook) setMessage(entry *logrus.Entry, data logrus.Fields) {
	if _, ok := data[hook.messageField]; ok {
		return
	}

	var v interface{} = entry.Message
	if fn, ok := hook.filters[hook.messageField]; ok {
		v = fn(v)
	}
	data[hook.messageField] = v
}

func setLevelString(entry *logrus.Entry, data logrus.Fields) {
	data["level"] = entry.Level.String()
}
