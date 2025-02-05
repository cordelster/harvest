// Copyright NetApp Inc, 2021 All rights reserved

package quota

import (
	"goharvest2/cmd/poller/collector"
	"goharvest2/cmd/poller/plugin"
	"goharvest2/pkg/api/ontapi/zapi"
	"goharvest2/pkg/conf"
	"goharvest2/pkg/dict"
	"goharvest2/pkg/errors"
	"goharvest2/pkg/matrix"
	"goharvest2/pkg/tree/node"
	"strconv"
	"strings"
)

const BatchSize = "500"

// Quota plugin is needed to match qtrees with quotas.
type Quota struct {
	*plugin.AbstractPlugin
	data           *matrix.Matrix
	instanceKeys   map[string]string
	instanceLabels map[string]*dict.Dict
	batchSize      string
	client         *zapi.Client
	query          string
}

func New(p *plugin.AbstractPlugin) plugin.Plugin {
	return &Quota{AbstractPlugin: p}
}

func (my *Quota) Init() error {

	var err error

	if err = my.InitAbc(); err != nil {
		return err
	}

	if my.client, err = zapi.New(conf.ZapiPoller(my.ParentParams)); err != nil {
		my.Logger.Error().Stack().Err(err).Msg("connecting")
		return err
	}

	if err = my.client.Init(5); err != nil {
		return err
	}

	my.query = "quota-report-iter"
	my.Logger.Debug().Msg("plugin connected!")

	my.data = matrix.New(my.Parent+".Qtree", "qtree", "qtree")
	my.instanceKeys = make(map[string]string)
	my.instanceLabels = make(map[string]*dict.Dict)

	exportOptions := node.NewS("export_options")
	instanceKeys := exportOptions.NewChildS("instance_keys", "")

	// apply all instance keys, instance labels from parent (qtree.yaml) to all quota metrics
	//parent instancekeys would be added in plugin metrics
	for _, parentKeys := range my.ParentParams.GetChildS("export_options").GetChildS("instance_keys").GetAllChildContentS() {
		instanceKeys.NewChildS("", parentKeys)
	}
	// parent instacelabels would be added in plugin metrics
	for _, parentLabels := range my.ParentParams.GetChildS("export_options").GetChildS("instance_labels").GetAllChildContentS() {
		instanceKeys.NewChildS("", parentLabels)
	}

	objects := my.Params.GetChildS("objects")
	if objects == nil {
		return errors.New(errors.MISSING_PARAM, "objects")
	}

	for _, obj := range objects.GetAllChildContentS() {
		metricName, display := collector.ParseMetricName(obj)

		metric, err := my.data.NewMetricFloat64(metricName)
		if err != nil {
			my.Logger.Error().Stack().Err(err).Msg("add metric")
			return err
		}

		metric.SetName(display)
		my.Logger.Debug().Msgf("added metric: (%s) [%s] %s", metricName, display, metric)
	}

	my.Logger.Debug().Msgf("added data with %d metrics", len(my.data.GetMetrics()))
	my.data.SetExportOptions(exportOptions)

	// batching the request
	if my.client.IsClustered() {
		if b := my.Params.GetChildContentS("batch_size"); b != "" {
			if _, err := strconv.Atoi(b); err == nil {
				my.batchSize = b
				my.Logger.Info().Msgf("using batch-size [%s]", my.batchSize)
			}
		} else {
			my.batchSize = BatchSize
			my.Logger.Trace().Str("BatchSize", BatchSize).Msg("Using default batch-size")
		}
	}

	return nil
}

func (my *Quota) Run(data *matrix.Matrix) ([]*matrix.Matrix, error) {

	var (
		request, result *node.Node
		quotas          []*node.Node
		tag             string
		err             error
	)

	var output []*matrix.Matrix

	// Purge and reset data
	my.data.PurgeInstances()
	my.data.Reset()

	// Set all global labels from zapi.go if already not exist
	my.data.SetGlobalLabels(data.GetGlobalLabels())

	request = node.NewXmlS(my.query)
	if my.client.IsClustered() && my.batchSize != "" {
		request.NewChildS("max-records", my.batchSize)
	}

	tag = "initial"

	for {
		result, tag, err = my.client.InvokeBatchRequest(request, tag)

		if err != nil {
			return nil, err
		}

		if result == nil {
			break
		}

		if x := result.GetChildS("attributes-list"); x != nil {
			quotas = x.GetChildren()
		}

		if len(quotas) == 0 {
			return nil, errors.New(errors.ERR_NO_INSTANCE, "no quota instances found")
		}

		my.Logger.Debug().Msgf("fetching %d quota counters", len(quotas))

		for quotaIndex, quota := range quotas {

			tree := quota.GetChildContentS("tree")
			volume := quota.GetChildContentS("volume")
			vserver := quota.GetChildContentS("vserver")

			// If quota-type is not a Qtree, then skip
			if quota.GetChildContentS("quota-type") != "tree" {
				continue
			}

			for attribute, m := range my.data.GetMetrics() {

				objectElem := quota.GetChildS(attribute)
				if objectElem == nil {
					my.Logger.Warn().Msgf("no [%s] instances on this %s.%s.%s", attribute, vserver, volume, tree)
					continue
				}

				if attrValue := quota.GetChildContentS(attribute); attrValue != "" {
					// Ex. InstanceKey: SVMA.vol1Abc.qtree1.5.disk-limit
					instanceKey := vserver + "." + volume + "." + tree + "." + strconv.Itoa(quotaIndex) + "." + attribute
					instance, err := my.data.NewInstance(instanceKey)

					if err != nil {
						my.Logger.Debug().Msgf("add (%s) instance: %v", attribute, err)
						return nil, err
					}

					my.Logger.Debug().Msgf("add (%s) instance: %s.%s.%s", attribute, vserver, volume, tree)

					qtreeInstance := data.GetInstance(tree + "." + volume + "." + vserver)
					for _, label := range my.data.GetExportOptions().GetChildS("instance_keys").GetAllChildContentS() {
						if value := qtreeInstance.GetLabel(label); value != "" {
							instance.SetLabel(label, value)
						}
					}

					// If the Qtree is the volume itself, than qtree label is empty, so copy the volume name to qtree.
					if tree == "" {
						instance.SetLabel("qtree", volume)
					}

					// populate numeric data
					if value := strings.Split(attrValue, " ")[0]; value != "" {
						// Few quota metrics would have value '-' which means unlimited (ex: disk-limit)
						if value == "-" {
							value = "0"
						}
						if err := m.SetValueString(instance, value); err != nil {
							my.Logger.Debug().Msgf("(%s) failed to parse value (%s): %v", attribute, value, err)
						} else {
							my.Logger.Debug().Msgf("(%s) added value (%s)", attribute, value)
						}
					}

				} else {
					my.Logger.Debug().Msgf("instance without [%s], skipping", attribute)
				}

				output = append(output, my.data)
			}
		}

	}
	return output, nil
}
