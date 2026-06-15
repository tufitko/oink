terraform {
  source = "../modules/storage"
}

dependency "net" {
  config_path = "../net"
  skip_outputs = true
}
