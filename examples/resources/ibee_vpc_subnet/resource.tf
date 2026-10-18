resource "ibee_vpc_subnet" "app" {
  vpc_id = "VPC_ID"
  name   = "app"
  cidr   = "10.0.1.0/24"
}
