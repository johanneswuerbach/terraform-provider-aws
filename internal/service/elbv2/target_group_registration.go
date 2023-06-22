package elbv2

import (
	"context"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-provider-aws/internal/create"
	"github.com/hashicorp/terraform-provider-aws/internal/framework"
	"github.com/hashicorp/terraform-provider-aws/internal/framework/flex"
	"github.com/hashicorp/terraform-provider-aws/internal/tfresource"
	"github.com/hashicorp/terraform-provider-aws/names"
)

// @FrameworkResource(name="Target Group Registration")
func newResourceTargetGroupRegistration(_ context.Context) (resource.ResourceWithConfigure, error) {
	return &resourceTargetGroupRegistration{}, nil
}

const (
	ResNameTargetGroupRegistration = "Target Group Registration"
)

type resourceTargetGroupRegistration struct {
	framework.ResourceWithConfigure
	framework.WithTimeouts
}

func (r *resourceTargetGroupRegistration) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "aws_lb_target_group_registration"
}

func (r *resourceTargetGroupRegistration) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": framework.IDAttribute(),
			"target_group_arn": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"target": schema.SetNestedBlock{
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"availability_zone": schema.StringAttribute{
							Optional: true,
						},
						"port": schema.Int64Attribute{
							Optional: true,
							Computed: true,
							PlanModifiers: []planmodifier.Int64{
								int64planmodifier.UseStateForUnknown(),
							},
						},
						"target_id": schema.StringAttribute{
							Required: true,
						},
					},
				},
			},
		},
	}
}

func (r *resourceTargetGroupRegistration) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	conn := r.Meta().ELBV2Conn(ctx)

	var plan resourceTargetGroupRegistrationData
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var tfList []targetData
	resp.Diagnostics.Append(plan.Target.ElementsAs(ctx, &tfList, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in := &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(plan.TargetGroupARN.ValueString()),
		Targets:        expandTargets(tfList),
	}

	_, err := conn.RegisterTargetsWithContext(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError(
			create.ProblemStandardMessage(names.ELBV2, create.ErrActionCreating, ResNameTargetGroupRegistration, plan.TargetGroupARN.String(), err),
			err.Error(),
		)
		return
	}
	// TODO: retries?

	// TODO: may need to read here to get computed port argument in cases where
	// it is omitted from configuration?

	plan.ID = types.StringValue(plan.TargetGroupARN.ValueString())

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *resourceTargetGroupRegistration) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	conn := r.Meta().ELBV2Conn(ctx)

	var state resourceTargetGroupRegistrationData
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var tfList []targetData
	resp.Diagnostics.Append(state.Target.ElementsAs(ctx, &tfList, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := findTargetGroupRegistrationByMultipartKey(ctx, conn, state.TargetGroupARN.ValueString(), tfList)
	if tfresource.NotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			create.ProblemStandardMessage(names.ELBV2, create.ErrActionSetting, ResNameTargetGroupRegistration, state.ID.String(), err),
			err.Error(),
		)
		return
	}

	// TODO: should this be used to re-write target state, or just as an error check?
	targets, d := flattenTargets(ctx, out)
	resp.Diagnostics.Append(d...)
	state.Target = targets

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *resourceTargetGroupRegistration) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	conn := r.Meta().ELBV2Conn(ctx)

	var plan, state resourceTargetGroupRegistrationData
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Target.Equal(state.Target) {

		var planTargets []targetData
		var stateTargets []targetData
		resp.Diagnostics.Append(plan.Target.ElementsAs(ctx, &planTargets, false)...)
		resp.Diagnostics.Append(plan.Target.ElementsAs(ctx, &stateTargets, false)...)
		if resp.Diagnostics.HasError() {
			return
		}

		planSet := flex.Set[targetData](planTargets)
		stateSet := flex.Set[targetData](planTargets)

		var regTargets, deregTargets []targetData
		for _, item := range planSet.Difference(stateSet) {
			deregTargets = append(deregTargets, item)
		}
		for _, item := range stateSet.Difference(planSet) {
			regTargets = append(regTargets, item)
		}
		// TODO: debug logging here - difference doesn't appear to be working?

		if len(deregTargets) > 0 {
			in := &elbv2.DeregisterTargetsInput{
				TargetGroupArn: aws.String(state.TargetGroupARN.ValueString()),
				Targets:        expandTargets(deregTargets),
			}

			_, err := conn.DeregisterTargetsWithContext(ctx, in)
			if err != nil {
				if tfawserr.ErrCodeEquals(err, elbv2.ErrCodeTargetGroupNotFoundException) {
					return
				}
				resp.Diagnostics.AddError(
					create.ProblemStandardMessage(names.ELBV2, create.ErrActionUpdating, ResNameTargetGroupRegistration, state.ID.String(), err),
					err.Error(),
				)
				return
			}
		}

		if len(regTargets) > 0 {
			in := &elbv2.RegisterTargetsInput{
				TargetGroupArn: aws.String(plan.TargetGroupARN.ValueString()),
				Targets:        expandTargets(regTargets),
			}

			_, err := conn.RegisterTargetsWithContext(ctx, in)
			if err != nil {
				resp.Diagnostics.AddError(
					create.ProblemStandardMessage(names.ELBV2, create.ErrActionUpdating, ResNameTargetGroupRegistration, plan.TargetGroupARN.String(), err),
					err.Error(),
				)
				return
			}
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *resourceTargetGroupRegistration) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	conn := r.Meta().ELBV2Conn(ctx)

	var state resourceTargetGroupRegistrationData
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var tfList []targetData
	resp.Diagnostics.Append(state.Target.ElementsAs(ctx, &tfList, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in := &elbv2.DeregisterTargetsInput{
		TargetGroupArn: aws.String(state.TargetGroupARN.ValueString()),
		Targets:        expandTargets(tfList),
	}

	_, err := conn.DeregisterTargetsWithContext(ctx, in)
	if err != nil {
		if tfawserr.ErrCodeEquals(err, elbv2.ErrCodeTargetGroupNotFoundException) {
			return
		}
		resp.Diagnostics.AddError(
			create.ProblemStandardMessage(names.ELBV2, create.ErrActionDeleting, ResNameTargetGroupRegistration, state.ID.String(), err),
			err.Error(),
		)
		return
	}
}

func (r *resourceTargetGroupRegistration) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func findTargetGroupRegistrationByMultipartKey(ctx context.Context, conn *elbv2.ELBV2, targetGroupARN string, targets []targetData) ([]*elbv2.TargetHealthDescription, error) {

	in := &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(targetGroupARN),
		Targets:        expandTargets(targets),
	}

	out, err := conn.DescribeTargetHealthWithContext(ctx, in)
	if tfawserr.ErrCodeEquals(err, elbv2.ErrCodeTargetGroupNotFoundException) {
		return nil, &retry.NotFoundError{
			LastError:   err,
			LastRequest: in,
		}
	}

	if err != nil {
		return nil, err
	}

	if out == nil || out.TargetHealthDescriptions == nil {
		return nil, tfresource.NewEmptyResultError(in)
	}

	return out.TargetHealthDescriptions, nil
}

func flattenTargets(ctx context.Context, apiObjects []*elbv2.TargetHealthDescription) (types.Set, diag.Diagnostics) {
	var diags diag.Diagnostics
	elemType := types.ObjectType{AttrTypes: targetAttrTypes}

	if len(apiObjects) == 0 {
		return types.SetNull(elemType), diags
	}

	elems := []attr.Value{}
	for _, apiObject := range apiObjects {
		if apiObject == nil || apiObject.Target == nil {
			continue
		}
		target := apiObject.Target

		obj := map[string]attr.Value{
			"availability_zone": flex.StringToFramework(ctx, target.AvailabilityZone),
			"port":              flex.Int64ToFramework(ctx, target.Port),
			"target_id":         flex.StringToFramework(ctx, target.Id),
		}
		objVal, d := types.ObjectValue(targetAttrTypes, obj)
		diags.Append(d...)

		elems = append(elems, objVal)
	}

	setVal, d := types.SetValue(elemType, elems)
	diags.Append(d...)

	return setVal, diags
}

func expandTargets(tfList []targetData) []*elbv2.TargetDescription {
	if len(tfList) == 0 {
		return nil
	}

	var apiObject []*elbv2.TargetDescription

	for _, tfObj := range tfList {
		item := &elbv2.TargetDescription{
			Id: aws.String(tfObj.TargetID.ValueString()),
		}
		if !tfObj.AvailabilityZone.IsNull() {
			item.AvailabilityZone = aws.String(tfObj.AvailabilityZone.ValueString())
		}
		if !tfObj.Port.IsNull() && !tfObj.Port.IsUnknown() {
			item.Port = aws.Int64(tfObj.Port.ValueInt64())
		}

		apiObject = append(apiObject, item)
	}

	return apiObject
}

type resourceTargetGroupRegistrationData struct {
	ID             types.String `tfsdk:"id"`
	Target         types.Set    `tfsdk:"target"`
	TargetGroupARN types.String `tfsdk:"target_group_arn"`
}

type targetData struct {
	AvailabilityZone types.String `tfsdk:"availability_zone"`
	Port             types.Int64  `tfsdk:"port"`
	TargetID         types.String `tfsdk:"target_id"`
}

var targetAttrTypes = map[string]attr.Type{
	"availability_zone": types.StringType,
	"port":              types.Int64Type,
	"target_id":         types.StringType,
}
