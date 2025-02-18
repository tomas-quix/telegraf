package opcua_listener

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/gopcua/opcua/ua"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/metric"
	"github.com/influxdata/telegraf/plugins/common/opcua"
	"github.com/influxdata/telegraf/plugins/common/opcua/input"
	"github.com/influxdata/telegraf/testutil"
)

const servicePort = "4840"

type OPCTags struct {
	Name           string
	Namespace      string
	IdentifierType string
	Identifier     string
	Want           interface{}
}

func MapOPCTag(tags OPCTags) (out input.NodeSettings) {
	out.FieldName = tags.Name
	out.Namespace = tags.Namespace
	out.IdentifierType = tags.IdentifierType
	out.Identifier = tags.Identifier
	return out
}

func TestInitPluginWithBadConnectFailBehaviorValue(t *testing.T) {
	plugin := OpcUaListener{
		SubscribeClientConfig: SubscribeClientConfig{
			InputClientConfig: input.InputClientConfig{
				OpcUAClientConfig: opcua.OpcUAClientConfig{
					Endpoint:       "opc.tcp://notarealserver:4840",
					SecurityPolicy: "None",
					SecurityMode:   "None",
					ConnectTimeout: config.Duration(5 * time.Second),
					RequestTimeout: config.Duration(10 * time.Second),
				},
				MetricName: "opcua",
				Timestamp:  input.TimestampSourceTelegraf,
				RootNodes:  make([]input.NodeSettings, 0),
			},
			ConnectFailBehavior:  "notanoption",
			SubscriptionInterval: config.Duration(100 * time.Millisecond),
		},
		Log: testutil.Logger{},
	}
	err := plugin.Init()
	require.ErrorContains(t, err, "unknown setting \"notanoption\" for 'connect_fail_behavior'")
}

func TestStartPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	acc := &testutil.Accumulator{}

	plugin := OpcUaListener{
		SubscribeClientConfig: SubscribeClientConfig{
			InputClientConfig: input.InputClientConfig{
				OpcUAClientConfig: opcua.OpcUAClientConfig{
					Endpoint:       "opc.tcp://notarealserver:4840",
					SecurityPolicy: "None",
					SecurityMode:   "None",
					ConnectTimeout: config.Duration(5 * time.Second),
					RequestTimeout: config.Duration(10 * time.Second),
				},
				MetricName: "opcua",
				Timestamp:  input.TimestampSourceTelegraf,
				RootNodes:  make([]input.NodeSettings, 0),
			},
			SubscriptionInterval: config.Duration(100 * time.Millisecond),
		},
		Log: testutil.Logger{},
	}
	testopctags := []OPCTags{
		{"ProductName", "0", "i", "2261", "open62541 OPC UA Server"},
	}
	for _, tags := range testopctags {
		plugin.SubscribeClientConfig.RootNodes = append(plugin.SubscribeClientConfig.RootNodes, MapOPCTag(tags))
	}
	require.NoError(t, plugin.Init())
	err := plugin.Start(acc)
	require.ErrorContains(t, err, "could not resolve address")

	plugin.SubscribeClientConfig.ConnectFailBehavior = "ignore"
	require.NoError(t, plugin.Init())
	require.NoError(t, plugin.Start(acc))
	require.Equal(t, opcua.Disconnected, plugin.client.OpcUAClient.State())
	plugin.Stop()

	container := testutil.Container{
		Image:        "open62541/open62541",
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("TCP network layer listening on opc.tcp://"),
		),
	}
	plugin.SubscribeClientConfig.ConnectFailBehavior = "retry"
	require.NoError(t, plugin.Init())
	require.NoError(t, plugin.Start(acc))
	require.Equal(t, opcua.Disconnected, plugin.client.OpcUAClient.State())

	err = container.Start()
	require.NoError(t, err, "failed to start container")

	defer container.Terminate()
	newEndpoint := fmt.Sprintf("opc.tcp://%s:%s", container.Address, container.Ports[servicePort])
	plugin.client.Config.Endpoint = newEndpoint
	plugin.client.OpcUAClient.Config.Endpoint = newEndpoint
	err = plugin.Gather(acc)
	require.NoError(t, err)
	require.Equal(t, opcua.Connected, plugin.client.OpcUAClient.State())
}

func TestSubscribeClientIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	container := testutil.Container{
		Image:        "open62541/open62541",
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("TCP network layer listening on opc.tcp://"),
		),
	}
	err := container.Start()
	require.NoError(t, err, "failed to start container")
	defer container.Terminate()

	testopctags := []OPCTags{
		{"ProductName", "0", "i", "2261", "open62541 OPC UA Server"},
		{"ProductUri", "0", "i", "2262", "http://open62541.org"},
		{"ManufacturerName", "0", "i", "2263", "open62541"},
		{"badnode", "1", "i", "1337", nil},
		{"goodnode", "1", "s", "the.answer", int32(42)},
		{"DateTime", "1", "i", "51037", "0001-01-01T00:00:00Z"},
	}
	tagsRemaining := make([]string, 0, len(testopctags))
	for i, tag := range testopctags {
		if tag.Want != nil {
			tagsRemaining = append(tagsRemaining, testopctags[i].Name)
		}
	}

	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       fmt.Sprintf("opc.tcp://%s:%s", container.Address, container.Ports[servicePort]),
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	for _, tags := range testopctags {
		subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, MapOPCTag(tags))
	}
	o, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.NoError(t, err)

	// give initial setup a couple extra attempts, as on CircleCI this can be
	// attempted to soon
	require.Eventually(t, func() bool {
		return o.SetupOptions() == nil
	}, 5*time.Second, 10*time.Millisecond)

	err = o.Connect()
	require.NoError(t, err, "Connection failed")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	res, err := o.StartStreamValues(ctx)
	require.Equal(t, opcua.Connected, o.State())
	require.NoError(t, err)

	for {
		select {
		case m := <-res:
			for fieldName, fieldValue := range m.Fields() {
				for _, tag := range testopctags {
					if fieldName != tag.Name {
						continue
					}

					if tag.Want == nil {
						t.Errorf("Tag: %s has value: %v", tag.Name, fieldValue)
						return
					}

					require.Equal(t, tag.Want, fieldValue)

					newRemaining := make([]string, 0, len(tagsRemaining))
					for _, remainingTag := range tagsRemaining {
						if fieldName != remainingTag {
							newRemaining = append(newRemaining, remainingTag)
							break
						}
					}

					if len(newRemaining) == 0 {
						return
					}

					tagsRemaining = newRemaining
				}
			}

		case <-ctx.Done():
			msg := ""
			for _, tag := range tagsRemaining {
				msg += tag + ", "
			}

			t.Errorf("Tags %s are remaining without a received value", msg)
			return
		}
	}
}

func TestSubscribeClientIntegrationAdditionalFields(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	container := testutil.Container{
		Image:        "open62541/open62541",
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("TCP network layer listening on opc.tcp://"),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	testopctags := []OPCTags{
		{"ProductName", "0", "i", "2261", "open62541 OPC UA Server"},
		{"ProductUri", "0", "i", "2262", "http://open62541.org"},
		{"ManufacturerName", "0", "i", "2263", "open62541"},
		{"badnode", "1", "i", "1337", nil},
		{"goodnode", "1", "s", "the.answer", int32(42)},
		{"DateTime", "1", "i", "51037", "0001-01-01T00:00:00Z"},
	}
	testopctypes := []string{
		"String",
		"String",
		"String",
		"Null",
		"Int32",
		"DateTime",
	}
	testopcquality := []string{
		"The operation succeeded. StatusGood (0x0)",
		"The operation succeeded. StatusGood (0x0)",
		"The operation succeeded. StatusGood (0x0)",
		"User does not have permission to perform the requested operation. StatusBadUserAccessDenied (0x801F0000)",
		"The operation succeeded. StatusGood (0x0)",
		"The operation succeeded. StatusGood (0x0)",
	}
	expectedopcmetrics := make([]telegraf.Metric, 0, len(testopctags))
	for i, x := range testopctags {
		now := time.Now()
		tags := map[string]string{
			"id": fmt.Sprintf("ns=%s;%s=%s", x.Namespace, x.IdentifierType, x.Identifier),
		}
		fields := map[string]interface{}{
			x.Name:     x.Want,
			"Quality":  testopcquality[i],
			"DataType": testopctypes[i],
		}
		expectedopcmetrics = append(expectedopcmetrics, metric.New("testing", tags, fields, now))
	}

	tagsRemaining := make([]string, 0, len(testopctags))
	for i, tag := range testopctags {
		if tag.Want != nil {
			tagsRemaining = append(tagsRemaining, testopctags[i].Name)
		}
	}

	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       fmt.Sprintf("opc.tcp://%s:%s", container.Address, container.Ports[servicePort]),
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
				OptionalFields: []string{"DataType"},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	for _, tags := range testopctags {
		subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, MapOPCTag(tags))
	}
	o, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.NoError(t, err)

	// give initial setup a couple extra attempts, as on CircleCI this can be
	// attempted to soon
	require.Eventually(t, func() bool {
		return o.SetupOptions() == nil
	}, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, o.Connect(), "Connection failed")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	res, err := o.StartStreamValues(ctx)
	require.NoError(t, err)

	for {
		select {
		case m := <-res:
			for fieldName, fieldValue := range m.Fields() {
				for _, tag := range testopctags {
					if fieldName != tag.Name {
						continue
					}
					// nil-value tags should not be sent from server, error if one does
					if tag.Want == nil {
						t.Errorf("Tag: %s has value: %v", tag.Name, fieldValue)
						return
					}

					newRemaining := make([]string, 0, len(tagsRemaining))
					for _, remainingTag := range tagsRemaining {
						if fieldName != remainingTag {
							newRemaining = append(newRemaining, remainingTag)
							break
						}
					}

					if len(newRemaining) == 0 {
						return
					}
					// Test if the received metric matches one of the expected
					testutil.RequireMetricsSubset(t, []telegraf.Metric{m}, expectedopcmetrics, testutil.IgnoreTime())
					tagsRemaining = newRemaining
				}
			}

		case <-ctx.Done():
			msg := ""
			for _, tag := range tagsRemaining {
				msg += tag + ", "
			}

			t.Errorf("Tags %s are remaining without a received value", msg)
			return
		}
	}
}

func TestSubscribeClientConfig(t *testing.T) {
	toml := `
[[inputs.opcua_listener]]
name = "localhost"
endpoint = "opc.tcp://localhost:4840"
connect_timeout = "10s"
request_timeout = "5s"
subscription_interval = "200ms"
connect_fail_behavior = "error"
security_policy = "auto"
security_mode = "auto"
certificate = "/etc/telegraf/cert.pem"
private_key = "/etc/telegraf/key.pem"
auth_method = "Anonymous"
timestamp_format = "2006-01-02T15:04:05Z07:00"
username = ""
password = ""

optional_fields = ["DataType"]

nodes = [
  {name="name",  namespace="1", identifier_type="s", identifier="one"},
  {name="name2", namespace="2", identifier_type="s", identifier="two"},
]

[[inputs.opcua_listener.group]]
name = "foo"
namespace = "3"
identifier_type = "i"
tags = [["tag1", "val1"], ["tag2", "val2"]]
nodes = [{name="name3", identifier="3000", tags=[["tag3", "val3"]]}]

[[inputs.opcua_listener.group]]
name = "bar"
namespace = "0"
identifier_type = "i"
tags = [["tag1", "val1"], ["tag2", "val2"]]
nodes = [{name="name4", identifier="4000", tags=[["tag1", "override"]]}]

[inputs.opcua_listener.workarounds]
additional_valid_status_codes = ["0xC0"]
`

	c := config.NewConfig()
	err := c.LoadConfigData([]byte(toml))
	require.NoError(t, err)

	require.Len(t, c.Inputs, 1)

	o, ok := c.Inputs[0].Input.(*OpcUaListener)
	require.True(t, ok)

	require.Equal(t, "localhost", o.SubscribeClientConfig.MetricName)
	require.Equal(t, "opc.tcp://localhost:4840", o.SubscribeClientConfig.Endpoint)
	require.Equal(t, config.Duration(10*time.Second), o.SubscribeClientConfig.ConnectTimeout)
	require.Equal(t, config.Duration(5*time.Second), o.SubscribeClientConfig.RequestTimeout)
	require.Equal(t, config.Duration(200*time.Millisecond), o.SubscribeClientConfig.SubscriptionInterval)
	require.Equal(t, "error", o.SubscribeClientConfig.ConnectFailBehavior)
	require.Equal(t, "auto", o.SubscribeClientConfig.SecurityPolicy)
	require.Equal(t, "auto", o.SubscribeClientConfig.SecurityMode)
	require.Equal(t, "/etc/telegraf/cert.pem", o.SubscribeClientConfig.Certificate)
	require.Equal(t, "/etc/telegraf/key.pem", o.SubscribeClientConfig.PrivateKey)
	require.Equal(t, "Anonymous", o.SubscribeClientConfig.AuthMethod)
	require.True(t, o.SubscribeClientConfig.Username.Empty())
	require.True(t, o.SubscribeClientConfig.Password.Empty())
	require.Equal(t, []input.NodeSettings{
		{
			FieldName:      "name",
			Namespace:      "1",
			IdentifierType: "s",
			Identifier:     "one",
		},
		{
			FieldName:      "name2",
			Namespace:      "2",
			IdentifierType: "s",
			Identifier:     "two",
		},
	}, o.SubscribeClientConfig.RootNodes)
	require.Equal(t, []input.NodeGroupSettings{
		{
			MetricName:     "foo",
			Namespace:      "3",
			IdentifierType: "i",
			TagsSlice:      [][]string{{"tag1", "val1"}, {"tag2", "val2"}},
			Nodes: []input.NodeSettings{{
				FieldName:  "name3",
				Identifier: "3000",
				TagsSlice:  [][]string{{"tag3", "val3"}},
			}},
		},
		{
			MetricName:     "bar",
			Namespace:      "0",
			IdentifierType: "i",
			TagsSlice:      [][]string{{"tag1", "val1"}, {"tag2", "val2"}},
			Nodes: []input.NodeSettings{{
				FieldName:  "name4",
				Identifier: "4000",
				TagsSlice:  [][]string{{"tag1", "override"}},
			}},
		},
	}, o.SubscribeClientConfig.Groups)
	require.Equal(t, opcua.OpcUAWorkarounds{AdditionalValidStatusCodes: []string{"0xC0"}}, o.SubscribeClientConfig.Workarounds)
	require.Equal(t, []string{"DataType"}, o.SubscribeClientConfig.OptionalFields)
}

func TestSubscribeClientConfigWithMonitoringParams(t *testing.T) {
	toml := `
[[inputs.opcua_listener]]
name = "localhost"
endpoint = "opc.tcp://localhost:4840"
subscription_interval = "200ms"

[[inputs.opcua_listener.group]]
name = "foo"
namespace = "3"
identifier_type = "i"
tags = [["tag1", "val1"], ["tag2", "val2"]]
nodes = [{name="name3", identifier="3000", tags=[["tag3", "val3"]]}]

[inputs.opcua_listener.group.nodes.monitoring_params]
sampling_interval = "50ms"
queue_size = 10
discard_oldest = true

[inputs.opcua_listener.group.nodes.monitoring_params.data_change_filter]
trigger = "StatusValue"
deadband_type = "Absolute"
deadband_value = 100.0
`

	c := config.NewConfig()
	err := c.LoadConfigData([]byte(toml))
	require.NoError(t, err)

	require.Len(t, c.Inputs, 1)

	o, ok := c.Inputs[0].Input.(*OpcUaListener)
	require.True(t, ok)

	queueSize := uint32(10)
	discardOldest := true
	deadbandValue := 100.0
	require.Equal(t, []input.NodeGroupSettings{
		{
			MetricName:     "foo",
			Namespace:      "3",
			IdentifierType: "i",
			TagsSlice:      [][]string{{"tag1", "val1"}, {"tag2", "val2"}},
			Nodes: []input.NodeSettings{{
				FieldName:  "name3",
				Identifier: "3000",
				TagsSlice:  [][]string{{"tag3", "val3"}},
				MonitoringParams: input.MonitoringParameters{
					SamplingInterval: 50000000,
					QueueSize:        &queueSize,
					DiscardOldest:    &discardOldest,
					DataChangeFilter: &input.DataChangeFilter{
						Trigger:       "StatusValue",
						DeadbandType:  "Absolute",
						DeadbandValue: &deadbandValue,
					},
				},
			}},
		},
	}, o.SubscribeClientConfig.Groups)
}

func TestSubscribeClientConfigInvalidTrigger(t *testing.T) {
	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       "opc.tcp://localhost:4840",
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, input.NodeSettings{
		FieldName:      "foo",
		Namespace:      "3",
		Identifier:     "1",
		IdentifierType: "i",
		MonitoringParams: input.MonitoringParameters{
			DataChangeFilter: &input.DataChangeFilter{
				Trigger: "not_valid",
			},
		},
	})

	_, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.ErrorContains(t, err, "trigger 'not_valid' not supported, node 'ns=3;i=1'")
}

func TestSubscribeClientConfigMissingTrigger(t *testing.T) {
	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       "opc.tcp://localhost:4840",
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, input.NodeSettings{
		FieldName:      "foo",
		Namespace:      "3",
		Identifier:     "1",
		IdentifierType: "i",
		MonitoringParams: input.MonitoringParameters{
			DataChangeFilter: &input.DataChangeFilter{
				DeadbandType: "Absolute",
			},
		},
	})

	_, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.ErrorContains(t, err, "trigger '' not supported, node 'ns=3;i=1'")
}

func TestSubscribeClientConfigInvalidDeadbandType(t *testing.T) {
	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       "opc.tcp://localhost:4840",
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, input.NodeSettings{
		FieldName:      "foo",
		Namespace:      "3",
		Identifier:     "1",
		IdentifierType: "i",
		MonitoringParams: input.MonitoringParameters{
			DataChangeFilter: &input.DataChangeFilter{
				Trigger:      "Status",
				DeadbandType: "not_valid",
			},
		},
	})

	_, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.ErrorContains(t, err, "deadband_type 'not_valid' not supported, node 'ns=3;i=1'")
}

func TestSubscribeClientConfigMissingDeadbandType(t *testing.T) {
	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       "opc.tcp://localhost:4840",
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, input.NodeSettings{
		FieldName:      "foo",
		Namespace:      "3",
		Identifier:     "1",
		IdentifierType: "i",
		MonitoringParams: input.MonitoringParameters{
			DataChangeFilter: &input.DataChangeFilter{
				Trigger: "Status",
			},
		},
	})

	_, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.ErrorContains(t, err, "deadband_type '' not supported, node 'ns=3;i=1'")
}

func TestSubscribeClientConfigInvalidDeadbandValue(t *testing.T) {
	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       "opc.tcp://localhost:4840",
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	deadbandValue := -1.0
	subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, input.NodeSettings{
		FieldName:      "foo",
		Namespace:      "3",
		Identifier:     "1",
		IdentifierType: "i",
		MonitoringParams: input.MonitoringParameters{
			DataChangeFilter: &input.DataChangeFilter{
				Trigger:       "Status",
				DeadbandType:  "Absolute",
				DeadbandValue: &deadbandValue,
			},
		},
	})

	_, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.ErrorContains(t, err, "negative deadband_value not supported, node 'ns=3;i=1'")
}

func TestSubscribeClientConfigMissingDeadbandValue(t *testing.T) {
	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       "opc.tcp://localhost:4840",
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}
	subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, input.NodeSettings{
		FieldName:      "foo",
		Namespace:      "3",
		Identifier:     "1",
		IdentifierType: "i",
		MonitoringParams: input.MonitoringParameters{
			DataChangeFilter: &input.DataChangeFilter{
				Trigger:      "Status",
				DeadbandType: "Absolute",
			},
		},
	})

	_, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.ErrorContains(t, err, "deadband_value was not set, node 'ns=3;i=1'")
}

func TestSubscribeClientConfigValidMonitoringParams(t *testing.T) {
	subscribeConfig := SubscribeClientConfig{
		InputClientConfig: input.InputClientConfig{
			OpcUAClientConfig: opcua.OpcUAClientConfig{
				Endpoint:       "opc.tcp://localhost:4840",
				SecurityPolicy: "None",
				SecurityMode:   "None",
				AuthMethod:     "Anonymous",
				ConnectTimeout: config.Duration(10 * time.Second),
				RequestTimeout: config.Duration(1 * time.Second),
				Workarounds:    opcua.OpcUAWorkarounds{},
			},
			MetricName: "testing",
			RootNodes:  make([]input.NodeSettings, 0),
			Groups:     make([]input.NodeGroupSettings, 0),
		},
		SubscriptionInterval: 0,
	}

	var queueSize uint32 = 10
	discardOldest := true
	deadbandValue := 10.0
	subscribeConfig.RootNodes = append(subscribeConfig.RootNodes, input.NodeSettings{
		FieldName:      "foo",
		Namespace:      "3",
		Identifier:     "1",
		IdentifierType: "i",
		MonitoringParams: input.MonitoringParameters{
			SamplingInterval: 50000000,
			QueueSize:        &queueSize,
			DiscardOldest:    &discardOldest,
			DataChangeFilter: &input.DataChangeFilter{
				Trigger:       "Status",
				DeadbandType:  "Absolute",
				DeadbandValue: &deadbandValue,
			},
		},
	})

	subClient, err := subscribeConfig.CreateSubscribeClient(testutil.Logger{})
	require.NoError(t, err)
	require.Equal(t, &ua.MonitoringParameters{
		SamplingInterval: 50,
		QueueSize:        queueSize,
		DiscardOldest:    discardOldest,
		Filter: ua.NewExtensionObject(
			&ua.DataChangeFilter{
				Trigger:       ua.DataChangeTriggerStatus,
				DeadbandType:  uint32(ua.DeadbandTypeAbsolute),
				DeadbandValue: deadbandValue,
			},
		),
	}, subClient.monitoredItemsReqs[0].RequestedParameters)
}
