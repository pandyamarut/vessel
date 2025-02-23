package vessel

import (
	"context"
	"fmt"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/deepfence/vessel/constants"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"net"
	"net/url"
	"strings"
)

func init() {
	customFormatter := new(logrus.TextFormatter)
	customFormatter.TimestampFormat = "2006-01-02 15:04:05"
	logrus.SetFormatter(customFormatter)
	customFormatter.FullTimestamp = true
}

// GetAddressAndDialer returns the address parsed from the given endpoint and a context dialer.
func GetAddressAndDialer(endpoint string) (string, func(ctx context.Context, addr string) (net.Conn, error), error) {
	protocol, addr, err := parseEndpointWithFallbackProtocol(endpoint, constants.UnixProtocol)
	if err != nil {
		return "", nil, err
	}
	if protocol != constants.UnixProtocol {
		return "", nil, fmt.Errorf("only support unix socket endpoint")
	}

	return addr, dial, nil
}

func dial(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, constants.UnixProtocol, addr)
}

func parseEndpointWithFallbackProtocol(endpoint string, fallbackProtocol string) (protocol string, addr string, err error) {
	if protocol, addr, err = parseEndpoint(endpoint); err != nil && protocol == "" {
		fallbackEndpoint := fallbackProtocol + "://" + endpoint
		protocol, addr, err = parseEndpoint(fallbackEndpoint)
		if err == nil {
			logrus.Warningf("Using %q as endpoint is deprecated, please consider using full url format %q.", endpoint, fallbackEndpoint)
		}
	}
	return
}

func parseEndpoint(endpoint string) (string, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", err
	}

	switch u.Scheme {
	case "tcp":
		return "tcp", u.Host, nil

	case "unix":
		return "unix", u.Path, nil

	case "":
		return "", "", fmt.Errorf("using %q as endpoint is deprecated, please consider using full url format", endpoint)

	default:
		return u.Scheme, "", fmt.Errorf("protocol %q not supported", u.Scheme)
	}
}

// getContainerRuntime returns the underlying container runtime and it's socket path
func getContainerRuntime(endPoints map[string]string) (string, string, error) {
	if endPoints == nil || len(endPoints) == 0 {
		return "", "", fmt.Errorf("endpoint is not set")
	}
	var detectedRuntime string
	var sockPath string
	for endPoint, runtime := range endPoints {
		logrus.Infof("trying to connect to endpoint '%s' with timeout '%s'", endPoint, constants.Timeout)
		addr, dialer, err := GetAddressAndDialer(endPoint)
		if err != nil {
			logrus.Warn(err)
			continue
		}

		if runtime == constants.DOCKER {
			_, err = net.DialTimeout(constants.UnixProtocol, addr, constants.Timeout)
			if err != nil {
				errMsg := errors.Wrapf(err, "could not connect to endpoint '%s'", endPoint)
				logrus.Warn(errMsg)
				continue
			}
			running, err := isDockerRunning(endPoint)
			if err != nil {
				logrus.Warn(err)
				continue
			}
			if !running {
				logrus.Warn(errors.New(fmt.Sprintf("no running containers found with endpoint %s", endPoint)))
				continue
			}
			logrus.Infof("connected successfully using endpoint: %s", endPoint)
			detectedRuntime = runtime
			sockPath = endPoint
			break
		} else {
			_, err = grpc.Dial(addr, grpc.WithInsecure(), grpc.WithBlock(), grpc.WithTimeout(constants.Timeout), grpc.WithContextDialer(dialer))
			if err != nil {
				errMsg := errors.Wrapf(err, "could not connect to endpoint '%s'", endPoint)
				logrus.Warn(errMsg)
				continue
			}
			running, err := isContainerdRunning(endPoint)
			if err != nil {
				logrus.Warn(err)
				continue
			}
			if !running {
				logrus.Warn(errors.New(fmt.Sprintf("no running containers found with endpoint %s", endPoint)))
				continue
			}
			logrus.Infof("connected successfully using endpoint: %s", endPoint)
			detectedRuntime = runtime
			sockPath = endPoint
			break
		}
	}
	return detectedRuntime, sockPath, nil
}

// AutoDetectRuntime auto detects the underlying container runtime like docker, containerd
func AutoDetectRuntime() (string, string, error) {
	runtime, sockPath, err := getContainerRuntime(constants.SupportedRuntimes)
	if err != nil {
		return "", "", err
	}
	if runtime == "" {
		return "", "", errors.New("could not detect container runtime")
	}
	logrus.Infof("container runtime detected: %s\n", runtime)
	return runtime, sockPath, nil
}

func isDockerRunning(host string) (bool, error) {
	dockerCli, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation(), client.WithHost(host), client.WithTimeout(constants.Timeout))
	if err != nil {
		return false, errors.Wrapf(err, " :error creating docker client")
	}
	defer dockerCli.Close()
	containers, err := dockerCli.ContainerList(context.Background(), types.ContainerListOptions{
		Quiet: true, All: true, Size: false,
	})
	if err != nil {
		return false, errors.Wrapf(err, " :error creating docker client")
	}

	if len(containers) > 0 {
		return true, nil
	}

	return false, nil
}

func isContainerdRunning(host string) (bool, error) {
	clientd, err := containerd.New(strings.Replace(host, "unix://", "", 1))
	if err != nil {
		return false, errors.Wrapf(err, " :error creating containerd client")
	}
	defer clientd.Close()

	// create a context for k8s with containerd namespace
	// TODO: using k8s ns, to support containerd standalone
	// make this configurable or autodetect
	k8s := namespaces.WithNamespace(context.Background(), constants.CONTAINERD_K8S_NS)

	containers, err := clientd.Containers(k8s)
	if err != nil {
		return false, errors.Wrapf(err, " :error creating containerd client")
	}

	if len(containers) > 0 {
		return true, nil
	}
	return false, nil
}
