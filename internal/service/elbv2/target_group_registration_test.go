package elbv2_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	sdkacctest "github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-provider-aws/internal/acctest"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	"github.com/hashicorp/terraform-provider-aws/internal/create"
	tfelbv2 "github.com/hashicorp/terraform-provider-aws/internal/service/elbv2"
	"github.com/hashicorp/terraform-provider-aws/names"
)

func TestAccELBV2TargetGroupRegistration_basic(t *testing.T) {
	ctx := acctest.Context(t)
	rName := sdkacctest.RandomWithPrefix(acctest.ResourcePrefix)
	resourceName := "aws_lb_target_group_registration.test"
	targetGroupResourceName := "aws_lb_target_group.test"

	resource.ParallelTest(t, resource.TestCase{
		PreCheck: func() {
			acctest.PreCheck(ctx, t)
			acctest.PreCheckPartitionHasService(t, elbv2.EndpointsID)
		},
		ErrorCheck:               acctest.ErrorCheck(t, elbv2.EndpointsID),
		ProtoV5ProviderFactories: acctest.ProtoV5ProviderFactories,
		CheckDestroy:             testAccCheckTargetGroupRegistrationDestroy(ctx),
		Steps: []resource.TestStep{
			{
				Config: testAccTargetGroupRegistrationConfig_basic(rName),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckTargetGroupRegistrationExists(ctx, resourceName),
					resource.TestCheckResourceAttrPair(resourceName, "target_group_arn", targetGroupResourceName, "arn"),
				),
			},
		},
	})
}

func TestAccELBV2TargetGroupRegistration_disappears(t *testing.T) {
	ctx := acctest.Context(t)
	rName := sdkacctest.RandomWithPrefix(acctest.ResourcePrefix)
	resourceName := "aws_lb_target_group_registration.test"

	resource.ParallelTest(t, resource.TestCase{
		PreCheck: func() {
			acctest.PreCheck(ctx, t)
			acctest.PreCheckPartitionHasService(t, elbv2.EndpointsID)
		},
		ErrorCheck:               acctest.ErrorCheck(t, elbv2.EndpointsID),
		ProtoV5ProviderFactories: acctest.ProtoV5ProviderFactories,
		CheckDestroy:             testAccCheckTargetGroupRegistrationDestroy(ctx),
		Steps: []resource.TestStep{
			{
				Config: testAccTargetGroupRegistrationConfig_basic(rName),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckTargetGroupRegistrationExists(ctx, resourceName),
					acctest.CheckFrameworkResourceDisappears(ctx, acctest.Provider, tfelbv2.ResourceTargetGroupRegistration, resourceName),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func testAccCheckTargetGroupRegistrationDestroy(ctx context.Context) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		conn := acctest.Provider.Meta().(*conns.AWSClient).ELBV2Conn(ctx)

		for _, rs := range s.RootModule().Resources {
			if rs.Type != "aws_lb_target_group_registration" && rs.Type != "aws_alb_target_group_registration" {
				continue
			}

			targetGroupArn := rs.Primary.Attributes["target_group_arn"]

			// Extracting target data from nested object string attributes is complicated, so
			// lazily describe with only the target group ARN input and check the resulting
			// output count instead.
			out, err := conn.DescribeTargetHealthWithContext(ctx, &elbv2.DescribeTargetHealthInput{
				TargetGroupArn: aws.String(targetGroupArn),
			})
			if err == nil {
				if len(out.TargetHealthDescriptions) != 0 {
					return fmt.Errorf("Target Group %q still has registered targets", rs.Primary.ID)
				}
			}

			if tfawserr.ErrCodeEquals(err, elbv2.ErrCodeTargetGroupNotFoundException) || tfawserr.ErrCodeEquals(err, elbv2.ErrCodeInvalidTargetException) {
				return nil
			}

			return create.Error(names.ELBV2, create.ErrActionCheckingDestroyed, tfelbv2.ResNameTargetGroupRegistration, rs.Primary.ID, errors.New("not destroyed"))
		}

		return nil
	}
}

func testAccCheckTargetGroupRegistrationExists(ctx context.Context, name string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return create.Error(names.ELBV2, create.ErrActionCheckingExistence, tfelbv2.ResNameTargetGroupRegistration, name, errors.New("not found"))
		}

		if rs.Primary.ID == "" {
			return create.Error(names.ELBV2, create.ErrActionCheckingExistence, tfelbv2.ResNameTargetGroupRegistration, name, errors.New("not set"))
		}

		conn := acctest.Provider.Meta().(*conns.AWSClient).ELBV2Conn(ctx)
		targetGroupArn := rs.Primary.Attributes["target_group_arn"]

		// Extracting target data from nested object string attributes is complicated, so
		// lazily describe with only the target group ARN input and check the resulting
		// output count instead.
		out, err := conn.DescribeTargetHealthWithContext(ctx, &elbv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(targetGroupArn),
		})
		if err != nil {
			return create.Error(names.ELBV2, create.ErrActionCheckingExistence, tfelbv2.ResNameTargetGroupRegistration, rs.Primary.ID, err)
		}
		if out.TargetHealthDescriptions == nil {
			return create.Error(names.ELBV2, create.ErrActionCheckingExistence, tfelbv2.ResNameTargetGroupRegistration, rs.Primary.ID, errors.New("empty response"))
		}
		// TODO: check length of registered targets against count of configured target blocks?

		return nil
	}
}

func testAccTargetGroupRegistrationConfig_basic(rName string) string {
	return acctest.ConfigCompose(
		acctest.ConfigLatestAmazonLinuxHVMEBSAMI(),
		acctest.AvailableEC2InstanceTypeForRegion("t3.micro", "t2.micro", "t1.micro", "m1.small"),
		acctest.ConfigVPCWithSubnets(rName, 1),
		fmt.Sprintf(`
resource "aws_instance" "test" {
  ami           = data.aws_ami.amzn-ami-minimal-hvm-ebs.id
  instance_type = data.aws_ec2_instance_type_offering.available.instance_type
  subnet_id     = aws_subnet.test[0].id
}

resource "aws_lb_target_group" "test" {
  name     = %[1]q
  port     = 80
  protocol = "HTTP"
  vpc_id   = aws_vpc.test.id
}

resource "aws_lb_target_group_registration" "test" {
  target_group_arn = aws_lb_target_group.test.arn

  target {
	target_id = aws_instance.test.id
  }
}
`, rName))
}
