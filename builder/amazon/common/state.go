package common

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/packer/helper/multistep"
)

// StateRefreshFunc is a function type used for StateChangeConf that is
// responsible for refreshing the item being watched for a state change.
//
// It returns three results. `result` is any object that will be returned
// as the final object after waiting for state change. This allows you to
// return the final updated object, for example an EC2 instance after refreshing
// it.
//
// `state` is the latest state of that object. And `err` is any error that
// may have happened while refreshing the state.
type StateRefreshFunc func() (result interface{}, state string, err error)

// StateChangeConf is the configuration struct used for `WaitForState`.
type StateChangeConf struct {
	Pending   []string
	Refresh   StateRefreshFunc
	StepState multistep.StateBag
	Target    string
}

// Provide context and timeout/retry configuration to AWS SDK's waiter.
func WaitUntilAMIAvailable(conn *ec2.EC2, imageId string) error {
	// use env vars to read in the wait delay and the max amount of time to wait
	delay := SleepSeconds()
	timeoutSeconds := TimeoutSeconds()
	// AWS sdk uses max attempts instead of a timeout; convert timeout into
	// max attempts
	maxAttempts := timeoutSeconds / delay

	imageInput := ec2.DescribeImagesInput{
		ImageIds: []*string{&imageId},
	}

	err := conn.WaitUntilImageAvailableWithContext(aws.BackgroundContext(),
		&imageInput,
		request.WithWaiterDelay(request.ConstantWaiterDelay(time.Duration(delay)*time.Second)),
		request.WithWaiterMaxAttempts(maxAttempts))
	return err
}

// Provide context and timeout/retry configuration to AWS SDK's waiter
func WaitUntilInstanceTerminated(conn *ec2.EC2, instanceId string) error {
	// use env vars to read in the wait delay and the max amount of time to wait
	delay := SleepSeconds()
	timeoutSeconds := TimeoutSeconds()
	// AWS sdk uses max attempts instead of a timeout; convert timeout into
	// max attempts
	maxAttempts := timeoutSeconds / delay

	instanceInput := ec2.DescribeInstancesInput{
		InstanceIds: []*string{&instanceId},
	}

	err := conn.WaitUntilInstanceTerminatedWithContext(aws.BackgroundContext(),
		&instanceInput,
		request.WithWaiterDelay(request.ConstantWaiterDelay(time.Duration(delay)*time.Second)),
		request.WithWaiterMaxAttempts(maxAttempts))
	return err
}

// Provide context and timeout/retry configuration to AWS SDK's waiter.
// This function works for both requesting and cancelling spot instances.
func WaitUntilSpotRequestFulfilled(conn *ec2.EC2, spotRequestId string) error {
	// use env vars to read in the wait delay and the max amount of time to wait
	delay := SleepSeconds()
	timeoutSeconds := TimeoutSeconds()
	// AWS sdk uses max attempts instead of a timeout; convert timeout into
	// max attempts
	maxAttempts := timeoutSeconds / delay

	spotRequestInput := ec2.DescribeSpotInstanceRequestsInput{
		SpotInstanceRequestIds: []*string{&spotRequestId},
	}

	err := conn.WaitUntilSpotInstanceRequestFulfilledWithContext(aws.BackgroundContext(),
		&spotRequestInput,
		request.WithWaiterDelay(request.ConstantWaiterDelay(time.Duration(delay)*time.Second)),
		request.WithWaiterMaxAttempts(maxAttempts))
	return err
}

func WaitUntilVolumeAvailable(conn *ec2.EC2, volumeId string) error {
	// use env vars to read in the wait delay and the max amount of time to wait
	delay := SleepSeconds()
	timeoutSeconds := TimeoutSeconds()
	// AWS sdk uses max attempts instead of a timeout; convert timeout into
	// max attempts
	maxAttempts := timeoutSeconds / delay

	volumeInput := ec2.DescribeVolumesInput{
		VolumeIds: []*string{&volumeId},
	}

	err := conn.WaitUntilVolumeAvailableWithContext(aws.BackgroundContext(),
		&volumeInput,
		request.WithWaiterDelay(request.ConstantWaiterDelay(time.Duration(delay)*time.Second)),
		request.WithWaiterMaxAttempts(maxAttempts))
	return err
}

func ImportImageRefreshFunc(conn *ec2.EC2, importTaskId string) StateRefreshFunc {
	return func() (interface{}, string, error) {
		resp, err := conn.DescribeImportImageTasks(&ec2.DescribeImportImageTasksInput{
			ImportTaskIds: []*string{
				&importTaskId,
			},
		},
		)
		if err != nil {
			if ec2err, ok := err.(awserr.Error); ok && strings.HasPrefix(ec2err.Code(), "InvalidConversionTaskId") {
				resp = nil
			} else if isTransientNetworkError(err) {
				resp = nil
			} else {
				log.Printf("Error on ImportImageRefresh: %s", err)
				return nil, "", err
			}
		}

		if resp == nil || len(resp.ImportImageTasks) == 0 {
			return nil, "", nil
		}

		i := resp.ImportImageTasks[0]
		return i, *i.Status, nil
	}
}

// WaitForState watches an object and waits for it to achieve a certain
// state.
func WaitForState(conf *StateChangeConf) (i interface{}, err error) {
	log.Printf("Waiting for state to become: %s", conf.Target)

	sleepSeconds := SleepSeconds()
	maxTicks := TimeoutSeconds()/sleepSeconds + 1
	notfoundTick := 0

	for {
		var currentState string
		i, currentState, err = conf.Refresh()
		if err != nil {
			return
		}

		if i == nil {
			// If we didn't find the resource, check if we have been
			// not finding it for awhile, and if so, report an error.
			notfoundTick += 1
			if notfoundTick > maxTicks {
				return nil, errors.New("couldn't find resource")
			}
		} else {
			// Reset the counter for when a resource isn't found
			notfoundTick = 0

			if currentState == conf.Target {
				return
			}

			if conf.StepState != nil {
				if _, ok := conf.StepState.GetOk(multistep.StateCancelled); ok {
					return nil, errors.New("interrupted")
				}
			}

			found := false
			for _, allowed := range conf.Pending {
				if currentState == allowed {
					found = true
					break
				}
			}

			if !found {
				err := fmt.Errorf("unexpected state '%s', wanted target '%s'", currentState, conf.Target)
				return nil, err
			}
		}

		time.Sleep(time.Duration(sleepSeconds) * time.Second)
	}
}

func isTransientNetworkError(err error) bool {
	if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
		return true
	}

	return false
}

// Returns 300 seconds (5 minutes) by default
// Some AWS operations, like copying an AMI to a distant region, take a very long time
// Allow user to override with AWS_TIMEOUT_SECONDS environment variable
func TimeoutSeconds() (seconds int) {
	seconds = 300

	override := os.Getenv("AWS_TIMEOUT_SECONDS")
	if override != "" {
		n, err := strconv.Atoi(override)
		if err != nil {
			log.Printf("Invalid timeout seconds '%s', using default", override)
		} else {
			seconds = n
		}
	}

	log.Printf("Allowing %ds to complete (change with AWS_TIMEOUT_SECONDS)", seconds)
	return seconds
}

// Returns 2 seconds by default
// AWS async operations sometimes takes long times, if there are multiple parallel builds,
// polling at 2 second frequency will exceed the request limit. Allow 2 seconds to be
// overwritten with AWS_POLL_DELAY_SECONDS
func SleepSeconds() (seconds int) {
	seconds = 2

	override := os.Getenv("AWS_POLL_DELAY_SECONDS")
	if override != "" {
		n, err := strconv.Atoi(override)
		if err != nil {
			log.Printf("Invalid sleep seconds '%s', using default", override)
		} else {
			seconds = n
		}
	}

	log.Printf("Using %ds as polling delay (change with AWS_POLL_DELAY_SECONDS)", seconds)
	return seconds
}
