package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithModifyPlan = (*vmBackupPolicyResource)(nil)

// Defaults must be applied after considering refreshed state. Static schema
// defaults would silently change imported/older policies (20:00 or hourly).
func backupScheduleDefaults(config, old vmBackupPolicyModel, next *vmBackupPolicyModel) {
	stringDefault := func(config, old types.String, next *types.String, fallback string) {
		if !config.IsNull() {
			return
		}
		if !old.IsNull() && !old.IsUnknown() {
			*next = old
		} else {
			*next = types.StringValue(fallback)
		}
	}
	intDefault := func(config, old types.Int64, next *types.Int64, fallback int64) {
		if !config.IsNull() {
			return
		}
		if !old.IsNull() && !old.IsUnknown() {
			*next = old
		} else {
			*next = types.Int64Value(fallback)
		}
	}
	stringDefault(config.Frequency, old.Frequency, &next.Frequency, "daily")
	stringDefault(config.Timezone, old.Timezone, &next.Timezone, "UTC")
	intDefault(config.Hour, old.Hour, &next.Hour, 12)
	intDefault(config.Minute, old.Minute, &next.Minute, 0)
	intDefault(config.WindowMinutes, old.WindowMinutes, &next.WindowMinutes, 30)
	if config.DayOfWeek.IsNull() {
		next.DayOfWeek = types.Int64Null()
		if next.Frequency.IsUnknown() {
			next.DayOfWeek = types.Int64Unknown()
		} else if next.Frequency.Equal(old.Frequency) && !old.DayOfWeek.IsUnknown() {
			next.DayOfWeek = old.DayOfWeek
		}
	}
}

func backupScheduleEqual(a, b vmBackupPolicyModel) bool {
	return a.Frequency.Equal(b.Frequency) && a.Timezone.Equal(b.Timezone) && a.Hour.Equal(b.Hour) && a.Minute.Equal(b.Minute) && a.DayOfWeek.Equal(b.DayOfWeek) && a.WindowMinutes.Equal(b.WindowMinutes)
}

func validateBackupSchedule(next vmBackupPolicyModel, old *vmBackupPolicyModel) error {
	// Untouched historical schedules are readable and can accompany retention
	// changes. Do not route them through SDK fallback normalization.
	if old != nil && backupScheduleEqual(*old, next) {
		return nil
	}
	if !next.Frequency.IsUnknown() {
		switch next.Frequency.ValueString() {
		case "daily":
			if !next.DayOfWeek.IsNull() && !next.DayOfWeek.IsUnknown() {
				return fmt.Errorf("day_of_week is only used with frequency = weekly")
			}
		case "weekly":
			if next.DayOfWeek.IsNull() {
				return fmt.Errorf("weekly schedules require day_of_week (Monday=0 through Sunday=6)")
			}
		case "hourly":
			return fmt.Errorf("new or changed hourly schedules are unsupported; an existing hourly schedule is preserved only while unchanged. To migrate, explicitly set frequency = daily or weekly and review hour, timezone and day_of_week before applying. Retention-only updates and destroy remain supported")
		default:
			return fmt.Errorf("new schedules require frequency = daily or weekly")
		}
	}
	if !next.Timezone.IsUnknown() {
		tz := next.Timezone.ValueString()
		if len(tz) == 0 || len(tz) > 128 || tz == "Local" {
			return fmt.Errorf("timezone must be a valid IANA timezone, for example UTC or Asia/Kolkata")
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("invalid IANA timezone: %w", err)
		}
	}
	for _, f := range []struct {
		name     string
		value    types.Int64
		min, max int64
	}{
		{"hour", next.Hour, 0, 23}, {"minute", next.Minute, 0, 59}, {"window_minutes", next.WindowMinutes, 5, 180}, {"day_of_week", next.DayOfWeek, 0, 6},
	} {
		if f.value.IsUnknown() || (f.name == "day_of_week" && f.value.IsNull()) {
			continue
		}
		if f.value.IsNull() || f.value.ValueInt64() < f.min || f.value.ValueInt64() > f.max {
			return fmt.Errorf("%s must be between %d and %d", f.name, f.min, f.max)
		}
	}
	return nil
}

func (r *vmBackupPolicyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var config, old, next vmBackupPolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &next)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	// Changing the VM creates a new policy; its omitted schedule uses new defaults.
	if !next.VmID.IsUnknown() && !old.VmID.IsNull() && !next.VmID.Equal(old.VmID) {
		old = vmBackupPolicyModel{}
	}
	backupScheduleDefaults(config, old, &next)
	var prior *vmBackupPolicyModel
	if !old.ID.IsNull() && !old.ID.IsUnknown() {
		prior = &old
	}
	if err := validateBackupSchedule(next, prior); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("frequency"), "Invalid backup schedule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &next)...)
}
