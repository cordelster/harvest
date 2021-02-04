package main

import (
    "errors"
    "strings"
    "strconv"
    "sync"
    "time"

    "goharvest2/poller/structs/matrix"
    "goharvest2/poller/structs/options"
    "goharvest2/poller/yaml"
    "goharvest2/poller/xml"
    "goharvest2/poller/share"
    "goharvest2/poller/share/logger"
    "goharvest2/poller/collector"

    client "goharvest2/poller/apis/zapi"
)

var Log *logger.Logger = logger.New(1, "")

type Zapi struct {
    *collector.AbstractCollector
    //name string
    //object string
    //options *opts.Opts
    //params *yaml.Node
    //Exporters []exporter.Exporter
    //Schedule *schedule.Schedule
    object_raw string
    connection client.Client
    system client.SystemInfo
    //TemplateFn string
    //Data *matrix.Matrix
    instanceKeyPrefix []string
}

func New(name, obj string, options *options.Options, params *yaml.Node) collector.Collector {
    a := collector.New(name, obj, options, params)
    return &Zapi{AbstractCollector: a}
}


func (c *Zapi) Init() error {

    var err error

    Log = logger.New(c.Options.LogLevel, c.Name+":"+c.Object)
    
    if c.connection, err = client.New(c.Params); err != nil {
        //Log.Error("connecting: %v", err)
        return err
    }

    if c.system, err = c.connection.GetSystemInfo(); err != nil {
        //Log.Error("system info: %v", err)
        return err
    }

    Log.Debug("Connected to: %s", c.system.String())

    template_fn := c.Params.GetChild("objects").GetChildValue(c.Object) // @TODO err handling

    template, err := collector.ImportObjectTemplate(c.Options.Path, "default", template_fn, c.Name, c.system.Version)
    if err != nil {
        Log.Error("Error importing subtemplate: %s", err)
        return err
    }
    c.Params.Union(template, false)
 
    if err := c.InitAbc(); err != nil {
        return err
    }

    if expopt := c.Params.GetChild("export_options"); expopt != nil {
        c.Data.SetExportOptions(expopt)
    } else {
        return errors.New("missing export options")
    }

    c.Metadata.AddMetric("api_time", "api_time", true) // extra metric for measuring api time

    if c.object_raw = c.Params.GetChildValue("object"); c.object_raw == "" {
        Log.Warn("Missing object in template")
    }

    c.Data.Object = c.object_raw
    c.Metadata.Object = c.object_raw
    
    counters := c.Params.GetChild("counters")
    if counters == nil {
        Log.Warn("Missing counters in template")
    }

    if c.object_raw == "" || counters == nil {
        return errors.New("missing parameters")
    }

    counters.PrintTree(0)
    Log.Debug("Parsing counters: %d values, %d children", len(counters.Values), len(counters.Children))
    ParseCounters(c.Data, counters, make([]string, 0))
    Log.Debug("Built counter cache with %d Metrics and %d Labels", c.Data.MetricsIndex+1, len(c.Data.Instances))

    if len(c.Data.InstanceKeys) == 0 {
        Log.Error("No instance keys in template")
        return errors.New("invalid parameters")
    }

    old_prefix := ParseKeyPrefix(c.Data.GetInstanceKeys())
    c.instanceKeyPrefix = ParseShortestPath(c.Data)
    Log.Debug("Parsed Instance Keys: %v", c.Data.InstanceKeys)
    Log.Debug("Parsed Instance Key Prefix: %v", c.instanceKeyPrefix)

    Log.Debug("Old Instance Key Prefix: %v", old_prefix)

    return nil

}

func (c *Zapi) Start(wg *sync.WaitGroup) {

    defer wg.Done()

    for {

        c.Metadata.InitData()

        for _, task := range c.Schedule.GetTasks() {

            if c.Schedule.IsDue(task) {

                c.Schedule.Start(task)

                data, err := c.poll(task)

                if err != nil {
                    Log.Warn("%s poll failed: %v", task, err)
                    return
                }
                
                Log.Debug("%s poll completed", task)

                duration := c.Schedule.Stop(task)
                c.Metadata.SetValueForMetricAndInstance("poll_time", task, duration.Seconds())
                
                if data != nil {
                    
                    Log.Debug("exporting to %d exporters", len(c.Exporters))

                    for _, e := range c.Exporters {
                        if err := e.Export(data); err != nil {
                            Log.Warn("export to [%s] failed: %v", e.GetName(), err)
                        }
                    }
                }
            }

            Log.Debug("exporting metadata")

            for _, e := range c.Exporters {
                if err := e.Export(c.Metadata); err != nil {
                    Log.Warn("Metadata export to [%s] failed: %v", e.GetName(), err)
                }
            }
        }

        d := c.Schedule.SleepDuration()
        Log.Debug("Sleeping %s until next poll session", d.String())
        c.Schedule.Sleep()
    }
}

func (c *Zapi) poll(task string) (*matrix.Matrix, error) {
    switch task {
        case "data":
            return c.poll_data()
        case "instance":
            return nil, c.poll_instance()
        default:
            return nil, errors.New("invalid task: " + task)
    }
}

func (c *Zapi) poll_instance() error {
    var err error
    var root *xml.Node
    var instances []*xml.Node
    var old_count int
    var keys []string
    var keypaths [][]string
    var found bool

    Log.Debug("starting instance poll")

    start := time.Now()
    c.connection.BuildRequest(xml.New(c.Params.GetChildValue("query")))
    root, err = c.connection.InvokeRequest()
    end := time.Since(start)

    c.Metadata.SetValueForMetricAndInstance("api_time", "instance", end.Seconds())

    if err != nil {
        Log.Error("client request failed: %s", err)
        return err
    }

    old_count = len(c.Data.Instances)
    c.Data.ResetInstances()

    instances = xml.SearchByPath(root, c.instanceKeyPrefix)
    Log.Debug("Fetched %d instances!!!!", len(instances))
    keypaths = c.Data.GetInstanceKeys()

    Log.Debug("keys=%v keypaths=%v found=%v", keys, keypaths, found)

    count := 0

    for _, instance := range instances {
        //c.Log.Printf("Handling instance element <%v> [%s]", &instance, instance.GetName())
        keys, found = xml.SearchByNames(instance, c.instanceKeyPrefix, keypaths)
        Log.Debug("Fetched instance keys (%v): %s", keypaths, strings.Join(keys, "."))

        if !found {
            Log.Debug("Skipping instance, keys not found:")
            xml.PrintTree(instance, 0)
        } else {
            _, err = c.Data.AddInstance(strings.Join(keys, "."))
            if err != nil {
                Log.Error("Error adding instance: %s", err)
            } else {
                Log.Debug("Added new Instance to cache [%s]", strings.Join(keys, "."))
                count += 1
            }
        }
        //xmltree.PrintTree(instance, 0)
        //break
    }

    c.Metadata.SetValueForMetricAndInstance("count", "instance", float64(count))

    //c.data.PrintInstances()
    Log.Info("added %d instances to cache (old cache had %d)", count, old_count)

    if len(c.Data.Instances) == 0 {
        Log.Info("Enterying standby mode until instances are detected")
        c.Status = "standby"
        c.Message = "no instances"
        c.Schedule.SetStandbyMode("instance", 1 * time.Hour)
    } else if c.Schedule.IsStandbyMode() {
        Log.Info("Unsetting standby mode")
        c.Status = "up"
        c.Message = ""
        c.Schedule.UnsetStandbyMode()
    }

    return nil
}

func (c *Zapi) poll_data() (*matrix.Matrix, error) {
    var err error
    var query string
    var node *xml.Node
    var fetch func(*matrix.Instance, *xml.Node, []string)
    var count, skipped int

    count = 0
    skipped = 0

    fetch = func(instance *matrix.Instance, node *xml.Node, path []string) {
        newpath := append(path, node.GetName())
        key := strings.Join(newpath, ".")
        metric, found := c.Data.GetMetric(key)
        content, has := node.GetContent()

        if has {
            if found {
                if float, err := strconv.ParseFloat(string(content), 64); err != nil {
                    Log.Warn("%sSkipping metric [%s]: failed to parse [%s] float%s", share.Red, key, content, share.End)
                    skipped += 1
                } else {
                    c.Data.SetValue(metric, instance, float)
                    Log.Trace("%sMetric [%s] - Set Value [%f]%s", share.Green, key, float, share.End)
                    count += 1
                }
            } else if label, found := c.Data.GetLabel(key); found {
                c.Data.SetInstanceLabel(instance, label, string(content))
                Log.Trace("%sMetric [%s] (%s) Set Value [%s] as Instance Label%s", share.Yellow, label, key, content, share.End)
                count += 1
            } else {
                Log.Trace("%sSkipped [%s]: not found in metric or label cache%s", share.Blue, key, share.End)
                skipped += 1
            }
        } else {
            Log.Trace("Skipping metric [%s] with no value", key)
            skipped += 1
        }

        for _, child := range node.GetChildren() {
            fetch(instance, child, newpath)
        }
    }

    Log.Debug("starting data poll")

    if err = c.Data.InitData(); err != nil {
        Log.Warn("Len metrics: %d", len(c.Data.Metrics))
        Log.Warn("Len instances: %d", len(c.Data.Instances))
        return nil, err
    }

    if query = c.Params.GetChildValue("query"); query == "" {
        return nil, errors.New("missing query in template")
    }

    c.connection.BuildRequest(xml.New(query))

    if node, err = c.connection.InvokeRequest(); err != nil {
        Log.Debug("Request for [%s] failed: %s", query, err)
        return nil, err
    }

    instances := xml.SearchByPath(node, c.instanceKeyPrefix)
    Log.Debug("Fetched %d instance elements", len(instances))

    for _, instance := range instances {
        //c.Log.Printf("Handling instance element <%v> [%s]", &instance, instance.GetName())
        keys, found := xml.SearchByNames(instance, c.instanceKeyPrefix, c.Data.GetInstanceKeys())
        Log.Debug("Fetched instance keys: %s", strings.Join(keys, "."))

        if !found {
            Log.Debug("Skipping instance: no keys fetched")
            continue
        }

        instanceObj, found := c.Data.GetInstance(strings.Join(keys, "."))

        if !found {
            Log.Debug("Skipping instance [%s]: not found in cache", strings.Join(keys, "."))
            continue
        }
        //path := make([]string, 0)
        //copy(path, c.InstanceKeyPrefix)
        fetch(instanceObj, instance, make([]string, 0))
    }
    //xmltree.PrintTree(node, 0)

    return c.Data, nil
}

/*

func News(name string, options *opts.Opts, params *yaml.Node) ([]*Zapi, error) {
    var subcollectors []*Zapi
    var err error

    Log = logger.New(options.LogLevel, class)
    
    connection, err := client.New(params)
    if err != nil {
        Log.Error("connecting: %v", err)
        return, subcollectors, err
    }

    system_info, err := connection.GetSystemInfo()
    if err != nil {
        Log.Error("system info: %v", err)
        return subcollectors, err
    }

    template, err := abc.ImportTemplate(options.Path, name)
    if err != nil {
        Log.Error("load template: %v", err)
        return subcollectors, err
    }

    objects := template.GetChild("objects")
    if objects == nil {
        Log.Error("no objects in template")
        return subcollectors, errors.New("no objects in template")
    }

    params.Union(template, false)

    for _, object := range objects.GetChildren() {
        c := New(name, object, optionos, params.Copy())
        c.system = system_info
        Log.Debug("Initialized subcollector [%s:%s]", name, c.object)
        subcollectors = append(subcollectors, c)
    }

    return subcollectors
}
*/