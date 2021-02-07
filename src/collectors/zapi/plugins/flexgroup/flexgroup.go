package main

import (
	"strings"
	"strconv"
	"goharvest2/poller/collector/plugin"
    "goharvest2/poller/struct/matrix"
	"goharvest2/poller/struct/options"
	"goharvest2/poller/struct/yaml"
	"goharvest2/poller/util/logger"
)

var Log *logger.Logger = logger.New(1, "")

type FlexGroup struct {
	*plugin.AbstractPlugin
}

func New(parent_name string, options *options.Options, params *yaml.Node, pparams *yaml.Node) plugin.Plugin {
	p := plugin.New(parent_name, options, params, pparams)
	return &FlexGroup{AbstractPlugin: p}
}


func fetch_names(instance *matrix.Instance) (string, string, string, string) {
	var key, name, svm, vol string

	if instance.Labels.Get("style") == "flexgroup_constituent" {
		if vol = instance.Labels.Get("volume_name"); len(vol) > 6 {
			name = vol[:len(vol)-6]
			svm = instance.Labels.Get("vserver_name")
			key = svm + "." + name
		}
	}

	return key, name, svm, vol
}

func (p *FlexGroup) Run(data *matrix.Matrix) ([]*matrix.Matrix, error) {

	n := data.Clone()
	n.Plugin = p.Name
	n.ResetInstances()

	counts := make(map[string]int)

	// create new instance cache
	for _, i := range data.GetInstances() {

		if key, name, svm, vol := fetch_names(i); key != "" {

			if instance := n.GetInstance(key); instance == nil {
				
				instance, err := n.AddInstance(key)

				if err != nil {
					Log.Error(err.Error())
					continue
				}

				instance.Name = name
				instance.Labels.Set("style", "flexgroup")
				instance.Labels.Set("volume", vol)
				instance.Labels.Set("vserver", svm)
				instance.Labels.Set("node", i.Labels.Get("node"))

				counts[key] = 1
			} else {
				counts[key] += 1
			}
		}
	}

	Log.Debug("extracted %d flexgroup instances", len(counts))

	n.InitData()

	// create summaries
	for _, i := range data.GetInstances() {
		if key, _, _, _ := fetch_names(i); key != "" {
			if instance := n.GetInstance(key); instance != nil {
				n.InstanceWiseAddition(instance, i, data)
			}
		}
	}

	// normalize percentage counters
	for key, instance := range n.GetInstances() {

		// set count as label
		count, _ := counts[key]
		instance.Labels.Set("count", strconv.Itoa(count))

		for _, metric := range n.GetMetrics() {
			if strings.Contains(metric.Display, "percent") {
				if value, has := n.GetValue(metric, instance); has {
					n.SetValue(metric, instance, value / float64(count))
				}
			}
		}
	}

	result := make([]*matrix.Matrix, 1)
	result[0] = n
	return result, nil
}