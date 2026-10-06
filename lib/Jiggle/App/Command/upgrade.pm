package Jiggle::App::Command::upgrade;
use v5.36;

use Jiggle::App -command;

use Jiggle::Library;
use Jiggle::Upgrade;

sub abstract { 'bring the library up to the format this jiggle needs' }

sub description {
  <<~'END';
  A library's metadata format changes now and then, and jiggle won't work on
  a library in an older format until it's upgraded.  This upgrades it,
  committing the changes to meta/, and records the new format in
  jiggle.toml.  If it's interrupted, run it again.
  END
}

sub execute ($self, $opt, $args) {
  my $library = Jiggle::Library->new({
    root   => $self->app->library_root,
    logger => $self->logger,
    allow_old_format => 1,
  });

  my @done = Jiggle::Upgrade->new({ library => $library })->upgrade;
  say @done ? map {; "upgraded $_" } @done : "the library is already format $Jiggle::Library::FORMAT";
}

1;
