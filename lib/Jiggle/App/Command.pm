package Jiggle::App::Command;
use v5.36;

use App::Cmd::Setup -command;

sub library ($self) { $self->app->library }

sub logger ($self) {
  return sub ($message) { say $message };
}

1;
