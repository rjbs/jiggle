package Jiggle::App::Command::dump;
use v5.36;

use Jiggle::App -command;

use JSON::MaybeXS ();
use Jiggle::Derive;
use Jiggle::Library;

sub abstract { 'print the library as this jiggle loads it, as JSON' }

sub description {
  <<~'END';
  This prints everything jiggle loads from the library's metadata: the
  site's configuration, every photo and every album, as one JSON document.
  It's for checking the migration to the Go server, which makes a database
  from the same files:

    jiggle dump | jiggle-go check --db FILE

  Each photo also says whether its renditions are all made, and how large
  each one is, since the migration records those, too.
  END
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('dump takes no arguments') if @$args;
}

sub execute ($self, $opt, $args) {
  # Not the app's library, whose logger writes to standard output, where
  # the JSON is going.
  my $library = Jiggle::Library->new({ root => $self->app->library_root });

  binmode STDOUT, ':raw';
  print JSON::MaybeXS->new->canonical->utf8->encode(dump_library($library)), "\n";
}

=func dump_library

  my $data = Jiggle::App::Command::dump::dump_library($library);

This returns the library's configuration, photos and albums as a structure
to be made into JSON.  Every key is always present, with a null for a value
the library doesn't have, and every value is given its type on purpose: an
id made of digits must be a string in the JSON, and a count a number.

=cut

my sub str  ($v) { defined $v ? "$v"  : undef }
my sub num  ($v) { defined $v ? 0 + $v : undef }
my sub bool ($v) { $v ? JSON::MaybeXS::true : JSON::MaybeXS::false }

sub dump_library ($library) {
  my $config = $library->config;
  my $derive = Jiggle::Derive->new({ library => $library });

  my @photos = map {; _dump_photo($_, $derive) }
               sort {; $a->id cmp $b->id } $library->photos;

  my @albums = map {;
    +{
      slug        => str($_->slug),
      title       => str($_->title),
      description => str($_->description),
      created     => str($_->created),
      cover       => str($_->cover),
      flickr_id   => str($_->flickr_id),
      photos      => [ map {; str($_) } $_->photos->@* ],
    }
  } sort {; $a->slug cmp $b->slug } $library->albums;

  return {
    config => {
      (map {; $_ => str($config->{$_}) } qw( title base_url author )),
      location_precision => num($config->{location_precision}),
      private_zone => [
        map {; +{ lat => num($_->{lat}), lon => num($_->{lon}), radius => num($_->{radius}) } }
        ($config->{private_zone} // [])->@*
      ],
    },
    photos => \@photos,
    albums => \@albums,
  };
}

sub _dump_photo ($photo, $derive) {
  my $original = $photo->original;
  my $location = $photo->location;

  return {
    (map {; $_ => str($photo->$_) } qw(
      id type title description taken visibility flickr_id flickr_uploaded added
    )),
    rotate => num($photo->rotate),
    tags   => [ map {; str($_) } $photo->tags->@* ],

    original => {
      (map {; $_ => str($original->{$_}) } qw( file ext sha256 source_mtime )),
      (map {; $_ => num($original->{$_}) } qw( bytes width height duration )),
    },

    location => $location && {
      lat     => num($location->{lat}),
      lon     => num($location->{lon}),
      private => bool($location->{private}),
    },

    rendered => bool($derive->is_complete($photo)),
    rendition_sizes => {
      map {; $_->{name} => [ Jiggle::Derive->rendition_size($photo, $_->{name}) ] }
      Jiggle::Derive->recipes_for($photo)
    },
  };
}

1;
