/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package claw

const (
	CONTAINER_NAME      = "tabclaw"
	INIT_CONTAINER_NAME = "init-tabclaw"

	TAB_CLAW              = "tabtabai.com/tabclaw"
	TAB_CLAW_NAME         = "tabtabai.com/tabclaw-name"
	TAB_CLAW_OCCUPIED     = "tabtabai.com/tabclaw-occupied"
	TAB_CLAW_INIT_TRIGGER = "tabtabai.com/tabclaw-init"

	ScheduledStopTime        = "tabtab.app.scheduled.deletion.time"
	ScheduledStopTimeTrigger = "tabtab.app.scheduled.deletion"

	CLAW_VOLUME_PREFIX = "claw:volume"
	CLAW_FINALIZERS    = "tabtabai.com/auto-remove-pv"
)
