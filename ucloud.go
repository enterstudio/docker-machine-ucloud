package ucloud

import (
	"fmt"
	"strings"
	"time"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/docker/machine/libmachine/state"
)

type Driver struct {
	*drivers.BaseDriver

	PublicKey  string
	PrivateKey string
	Region     string
	ImageId    string
	Password   string
	UhostID    string

	CPU       int
	Memory    int
	DiskSpace int

	PrivateIPOnly     bool
	PrivateIPAddress  string
	SecurityGroupId   int
	SecurityGroupName string
}

const (
	defaultTimeout   = 1 * time.Second
	defaultCPU       = 1
	defaultMemory    = 1024
	defaultDiskSpace = 20000
	defaultRegion    = "cn-north-03"
	defaultRetries   = 10
	defaultImageId   = "uimage-5yt2b0" // we use CentOS 7.0 default
)

func NewDriver(hostName, artifactPath string) *Driver {
	return &Driver{
		BaseDriver: &drivers.BaseDriver{
			MachineName:  hostName,
			ArtifactPath: artifactPath,
		},
		Region:    defaultRegion,
		Memory:    defaultMemory,
		CPU:       defaultCPU,
		DiskSpace: defaultDiskSpace,
	}
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		{
			Name:   "ucloud-public-key",
			Usage:  "UCloud Public Key",
			Value:  "",
			EnvVar: "UCLOUD_PUBLIC_KEY",
		},
		{
			Name:   "ucloud-private-key",
			Usage:  "UCloud Private Key",
			Value:  "",
			EnvVar: "UCLOUD_PRIVATE_KEY",
		},
		{
			Name:  "ucloud-imageid",
			Usage: "UHost image id",
			Value: "",
		},
		{
			Name:  "ucloud-user-password",
			Usage: "Password of ucloud user",
			Value: "",
		},
		{
			Name:   "ucloud-region",
			Usage:  "Region of ucloud idc",
			Value:  "cn-north-03",
			EnvVar: "UCLOUD_REGION",
		},
		{
			Name:  "ucloud-ssh-user",
			Usage: "SSH user",
			Value: "root",
		},
		{
			Name:  "ucloud-ssh-port",
			Usage: "SSH port",
			Value: 22,
		},
		{
			Name:  "ucloud-private-address-only",
			Usage: "Only use a private IP address",
		},
		{
			Name:  "ucloud-security-group",
			Usage: "UCloud security group",
			Value: "docker-machine",
		},
	}
}

func (d *Driver) DriverName() string {
	return "ucloud"
}

func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

func (d *Driver) GetSSHUsername() string {
	if d.SSHUser == "" {
		d.SSHUser = "root"
	}

	return d.SSHUser
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	region, err := validateUCloudRegion(flags.String("ucloud-region"))
	if err != nil {
		return err
	}
	d.Region = region

	d.PublicKey = flags.String("ucloud-public-key")
	if d.PublicKey == "" {
		return fmt.Errorf("ucloud driver requires the --ucloud-public-key option")
	}
	log.Debugf("ucloud public key: %s", d.PublicKey)

	d.PrivateKey = flags.String("ucloud-private-key")
	if d.PrivateKey == "" {
		return fmt.Errorf("ucloud driver requires the --ucloud-private-key option")
	}
	log.Debugf("ucloud private key: %s", d.PrivateKey)

	image := flags.String("ucloud-imageid")
	if len(image) == 0 {
		image = defaultImageId
	}
	d.ImageId = image

	d.PrivateIPOnly = flags.Bool("ucloud-private-address-only")
	d.SecurityGroupName = flags.String("ucloud-security-group")

	d.SSHUser = strings.ToLower(flags.String("ucloud-ssh-user"))
	if d.SSHUser == "" {
		d.SSHUser = "root"
	}
	d.Password = flags.String("ucloud-user-password")
	d.SSHPort = 22

	return nil
}

func (d *Driver) PreCreateCheck() error {
	return nil
}

func (d *Driver) Create() error {
	log.Infof("Create UHost instance...")

	if d.Password == "" {
		return fmt.Errorf("ucloud driver requires --ucloud-user-password options.")
	}

	// create keypair
	log.Infof("Creating key pair for instances...")
	if err := d.createKeyPair(); err != nil {
		return fmt.Errorf("unable to create key pair: %s", err)
	}

	// create uhost instance
	log.Infof("Creating uhost instance...")
	if err := d.createUHost(); err != nil {
		return fmt.Errorf("create UHost failed:%s", err)
	}

	// waiting for creating successful
	if err := mcnutils.WaitFor(drivers.MachineInState(d, state.Running)); err != nil {
		return fmt.Errorf("wait for machine running failed: %s", err)
	}

	// create networks, like private ip, eip, and security group
	log.Infof("Creating networks...")
	//TODO: user the exist eip and security group to configure network
	if err := d.createUNet(); err != nil {
		return fmt.Errorf("create networks failed:%s", err)
	}

	// upload keypair
	if err := d.uploadKeyPair(); err != nil {
		return fmt.Errorf("upload keypair failed:%s", err)
	}

	// TODO: get detail info of uhost to save
	return nil
}

func (d *Driver) GetURL() (string, error) {
	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("tcp://%s:2376", ip), nil
}

func (d *Driver) GetIP() (string, error) {
	if d.IPAddress == "" {
		return "", fmt.Errorf("IP address is not set")
	}

	return d.IPAddress, nil
}

func (d *Driver) GetState() (state.State, error) {
	log.Debugf("Get Uhost details")
	if d.UhostID == "" || d.Region == "" {
		return state.None, fmt.Errorf("region or uhost is empty")
	}

	details, err := d.getHostDescription()
	if err != nil {
		return state.None, fmt.Errorf("get UHost details failed:%s", err)
	}

	var st state.State
	if details != nil && details.state != "" {
		switch details.state {
		case "Initializing", "Starting", "Rebooting":
			st = state.Starting
		case "Running":
			st = state.Running
		case "Stopped":
			st = state.Stopped
		case "Stopping":
			st = state.Stopping
		case "Install Fail":
			st = state.Error
		default:
			st = state.None
		}
	}

	return st, nil
}

func (d *Driver) Start() error {
	log.Info("Start UHost...")
	err := startUHost(d.Region, d.UhostID)
	if err != nil {
		return fmt.Errorf("Cannot start Machine:%s, with UHost: %s.", d.MachineName, d.UhostID)
	}

	return nil
}

func (d *Driver) Stop() error {
	log.Info("Stop UHost...")
	if len(d.UhostID) == 0 {
		return fmt.Errorf("UHost is not exist for Machine:%s", d.MachineName)
	}

	err := stopUHost(d.Region, d.UhostID)
	if err != nil {
		return fmt.Errorf("Cannot start Machine:%s, with UHost: %s.", d.MachineName, d.UhostID)
	}

	return nil
}

func (d *Driver) Remove() error {
	log.Debug("Removing...")
	if err := terminateUHost(d.Region, d.UhostID); err != nil {
		return fmt.Errorf("Unable to terminate the UHost instance:%s", err)
	}

	//TODO: any cleanup ?
	return nil
}

func (d *Driver) Restart() error {
	log.Debug("Restarting...")
	if err := rebootUHost(d.Region, d.UhostID); err != nil {
		return fmt.Errorf("Unable to restart the UHost instance:%s", err)
	}

	return nil
}

func (d *Driver) Kill() error {
	log.Debug("Killing...")
	if err := killUHost(d.Region, d.UhostID); err != nil {
		return fmt.Errorf("Unable to kill the UHost instance:%s", err)
	}

	return nil
}