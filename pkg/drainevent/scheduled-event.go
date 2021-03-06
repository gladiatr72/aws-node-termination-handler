// Copyright 2016-2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package drainevent

import (
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-node-termination-handler/pkg/ec2metadata"
	"github.com/aws/aws-node-termination-handler/pkg/node"
)

const (
	// ScheduledEventKind is a const to define a scheduled event kind of drainable event
	ScheduledEventKind           = "SCHEDULED_EVENT"
	scheduledEventStateCompleted = "completed"
	scheduledEventStateCanceled  = "canceled"
	scheduledEventDateFormat     = "02 Jan 2006 15:04:05 GMT"
	instanceStopCode             = "instance-stop"
	systemRebootCode             = "system-reboot"
	instanceRebootCode           = "instance-reboot"
	instanceRetirementCode       = "instance-retirement"
	systemMaintenanceCode        = "system-maintenance"
)

// MonitorForScheduledEvents continuously monitors metadata for scheduled events and sends drain events to the passed in channel
func MonitorForScheduledEvents(drainChan chan<- DrainEvent, cancelChan chan<- DrainEvent, imds *ec2metadata.Service) error {
	drainEvents, err := checkForScheduledEvents(imds)
	if err != nil {
		return err
	}
	for _, drainEvent := range drainEvents {
		if isStateCanceledOrCompleted(drainEvent.State) {
			log.Println("Sending cancel events to the cancel channel")
			cancelChan <- drainEvent
		} else {
			log.Println("Sending drain events to the drain channel")
			drainChan <- drainEvent
		}
	}
	return nil
}

// checkForScheduledEvents Checks EC2 instance metadata for a scheduled event requiring a node drain
func checkForScheduledEvents(imds *ec2metadata.Service) ([]DrainEvent, error) {
	scheduledEvents, err := imds.GetScheduledMaintenanceEvents()
	if err != nil {
		return nil, fmt.Errorf("Unable to parse metadata response: %w", err)
	}
	events := make([]DrainEvent, 0)
	for _, scheduledEvent := range scheduledEvents {
		var preDrainFunc preDrainTask
		if isRestartEvent(scheduledEvent.Code) && !isStateCanceledOrCompleted(scheduledEvent.State) {
			preDrainFunc = uncordonAfterRebootPreDrain
		}
		notBefore, err := time.Parse(scheduledEventDateFormat, scheduledEvent.NotBefore)
		if err != nil {
			return nil, fmt.Errorf("Unable to parsed scheduled event start time: %w", err)
		}
		notAfter, err := time.Parse(scheduledEventDateFormat, scheduledEvent.NotAfter)
		if err != nil {
			return nil, fmt.Errorf("Unable to parsed scheduled event end time: %w", err)
		}
		events = append(events, DrainEvent{
			EventID:      scheduledEvent.EventID,
			Kind:         ScheduledEventKind,
			Description:  fmt.Sprintf("%s will occur between %s and %s because %s\n", scheduledEvent.Code, scheduledEvent.NotBefore, scheduledEvent.NotAfter, scheduledEvent.Description),
			State:        scheduledEvent.State,
			StartTime:    notBefore,
			EndTime:      notAfter,
			PreDrainTask: preDrainFunc,
		})
	}
	return events, nil
}

func uncordonAfterRebootPreDrain(drainEvent DrainEvent, node node.Node) error {
	err := node.MarkWithEventID(drainEvent.EventID)
	if err != nil {
		return fmt.Errorf("Unable to mark node with event ID: %w", err)
	}
	// if the node is already marked as unschedulable, then don't do anything
	unschedulable, err := node.IsUnschedulable()
	if err == nil && unschedulable {
		log.Println("Node is already marked unschedulable, not taking any action to add uncordon label.")
		return nil
	} else if err != nil {
		return fmt.Errorf("Encountered an error while checking if the node is unschedulable. Not setting an uncordon label: %w", err)
	}
	err = node.MarkForUncordonAfterReboot()
	if err != nil {
		return fmt.Errorf("Unable to mark the node for uncordon: %w", err)
	}
	log.Println("Successfully applied uncordon after reboot action label to node.")
	return nil
}

func isStateCanceledOrCompleted(state string) bool {
	return state == scheduledEventStateCanceled ||
		state == scheduledEventStateCompleted
}

func isRestartEvent(maintenanceCode string) bool {
	return maintenanceCode == instanceStopCode ||
		maintenanceCode == instanceRetirementCode ||
		maintenanceCode == instanceRebootCode ||
		maintenanceCode == systemRebootCode
}
