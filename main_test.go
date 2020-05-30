package main

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/spf13/viper"
)

func TestNoConfigFile(t *testing.T) {
	Expected := "open : no such file or directory"
	config := viper.New()
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}

func TestEmptyConfigFile(t *testing.T) {
	Expected := "EOF"
	cf, err := ioutil.TempFile("/tmp/", "testing")
	if err != nil {
		t.Errorf("Could not create tmp file:%v", err)
	}
	defer os.Remove(cf.Name())
	config := viper.New()
	config.SetDefault("config", cf.Name())
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}

func TestEmptyJsonConfigFile(t *testing.T) {
	Expected := "no streams provided"
	cf, err := ioutil.TempFile("/tmp/", "testing")
	if err != nil {
		t.Errorf("Could not create tmp file:%v", err)
	}
	defer os.Remove(cf.Name())
	cf.WriteString("{}")
	config := viper.New()
	config.SetDefault("config", cf.Name())
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}

func TestBadReadTimeConfigFile(t *testing.T) {
	Expected := "unable to parse read timeout: time: invalid duration xddas"
	cf, err := ioutil.TempFile("/tmp/", "testing")
	if err != nil {
		t.Errorf("Could not create tmp file:%v", err)
	}
	defer os.Remove(cf.Name())
	cf.WriteString("readTimeout: xddas\n")
	config := viper.New()
	config.SetDefault("config", cf.Name())
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}

func TestBadWriteTimeConfigFile(t *testing.T) {
	Expected := "unable to parse write timeout: time: invalid duration xddas"
	cf, err := ioutil.TempFile("/tmp/", "testing")
	if err != nil {
		t.Errorf("Could not create tmp file:%v", err)
	}
	defer os.Remove(cf.Name())
	cf.WriteString("writeTimeout: xddas\n")
	config := viper.New()
	config.SetDefault("config", cf.Name())
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}

func TestBadStreamProtosConfigFile(t *testing.T) {
	Expected := "unsupported protocol: 'xddas'"
	cf, err := ioutil.TempFile("/tmp/", "testing")
	if err != nil {
		t.Errorf("Could not create tmp file:%v", err)
	}
	defer os.Remove(cf.Name())
	cf.WriteString("server:\n  protocols: [xddas]\n")
	config := viper.New()
	config.SetDefault("config", cf.Name())
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}

// func TestNoStreamProtoConfigFile(t *testing.T) {
// 	Expected := "no protocols provided"
// 	cf, err := ioutil.TempFile("/tmp/", "testing")
// 	if err != nil {
// 		t.Errorf("Could not create tmp file:%v", err)
// 	}
// 	defer os.Remove(cf.Name())
// 	cf.WriteString("server:\n  protocols: []\n")
// 	config := viper.New()
// 	config.SetDefault("config", cf.Name())
// 	p, err := newProgram(config)
// 	if err.Error() != Expected {
// 		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
// 	}
// 	if p != nil {
// 		t.Errorf("Should not have newProgram")
// 	}
// }

func TestOddRTPConfigFile(t *testing.T) {
	Expected := "rtp port must be even"
	cf, err := ioutil.TempFile("/tmp/", "testing")
	if err != nil {
		t.Errorf("Could not create tmp file:%v", err)
	}
	defer os.Remove(cf.Name())
	cf.WriteString("server:\n  rtpPort: 443\n")
	config := viper.New()
	config.SetDefault("config", cf.Name())
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}

func TestCrazyRTCPConfigFile(t *testing.T) {
	Expected := "rtcp port must be rtp port plus 1"
	cf, err := ioutil.TempFile("/tmp/", "testing")
	if err != nil {
		t.Errorf("Could not create tmp file:%v", err)
	}
	defer os.Remove(cf.Name())
	cf.WriteString("server:\n  rtpPort: 444\n  rtcpPort: 3323")
	config := viper.New()
	config.SetDefault("config", cf.Name())
	p, err := newProgram(config)
	if err.Error() != Expected {
		t.Errorf("Error actual = %v, and Expected = %v.", err, Expected)
	}
	if p != nil {
		t.Errorf("Should not have newProgram")
	}
}
