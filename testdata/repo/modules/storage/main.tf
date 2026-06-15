resource "aws_s3_bucket_policy" "this" {
  bucket = "stub"
  policy = file("${path.module}/files/policy.json")
}

module "sub" {
  source = "./sub"
}
