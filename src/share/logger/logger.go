package logger

import (
    "fmt"
    "log"
    "os"
    "path"
    "strconv"
    "goharvest2/poller/errors"
)

const flags int = log.Ldate | log.Ltime | log.Lmsgprefix
const fileflags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY
const fileperm os.FileMode = 0644
const dirperm os.FileMode = 0755

var file *os.File

var levels = [6]string{ 
    " (trace  ) %-12s: ", 
    " (debug  ) %-12s: ", 
    " (info   ) %-12s: ", 
    " (warning) %-12s: ", 
    " (error  ) %-12s: ", 
    " (fatal  ) %-12s: ",
}

var level = 2

func OpenFileOutput(rootdir, filename string) error {
    var info os.FileInfo
    var err error

    info, err = os.Stat(path.Join(rootdir, "log"))
    if err != nil || info.IsDir() == true {
        err = os.Mkdir(path.Join(rootdir, "log"), dirperm)
    }
    if err == nil || os.IsExist(err) {

        file, err = os.OpenFile(path.Join(rootdir, "log", filename), fileflags, fileperm)
        if err == nil {
            log.SetOutput(file)
        } else {
        }
    }
    return err
}

func CloseFileOutput() error {
    return file.Close()
}

func SetLevel(l int) error {
    var err error
    if l > 0 && l < len(levels) {
        level = l
    } else {
        err = errors.New(errors.INVALID_PARAM, "level " + strconv.Itoa(level))
    }
    return err
}

func Log(lvl int, prefix, format string, vars... interface{}) {
    log.Printf(fmt.Sprintf(levels[lvl], prefix) + fmt.Sprintf(format, vars...))
}

func Trace(prefix, format string, vars... interface{}) {
    if level == 0 {
        Log(0, prefix, format, vars...)
    }
}

func Debug(prefix, format string, vars... interface{}) {
    if level <= 1 {
        Log(1, prefix, format, vars...)
    }
}

func Info(prefix, format string, vars... interface{}) {
    if level <= 2 {
        Log(2, prefix, format, vars...)
    }
}

func Warn(prefix, format string, vars... interface{}) {
    if level <= 3 {
        Log(3, prefix, format, vars...)
    }
}

func Error(prefix, format string, vars... interface{}) {
    if level <= 4 {
        Log(4, prefix, format, vars...)
    }
}

func Fatal(prefix, format string, vars... interface{}) {
    if level <= 5 {
        Log(5, prefix, format, vars...)
    }
}
