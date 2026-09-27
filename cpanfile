# External programs, not installable from CPAN: libvips (vips), ffmpeg,
# exiftool (from Image::ExifTool, below), and Pagefind (run via npx if it's
# not on PATH, so it needs Node).
#
# CommonMark links against libcmark (brew install cmark).  On Apple Silicon,
# Homebrew's prefix isn't on the default search path, so install it with:
#
#   P=$(brew --prefix cmark)
#   cpanm --configure-args="INC=-I$P/include LIBS='-L$P/lib -lcmark'" CommonMark

requires 'perl', '5.036';

requires 'App::Cmd', '0.339';
requires 'CommonMark';
requires 'Image::ExifTool';
requires 'JSON::MaybeXS';
requires 'List::Util', '1.50';
requires 'Mojolicious', '9';
requires 'Moo', '2';
requires 'Parallel::ForkManager';
requires 'Path::Tiny';
requires 'TOML::Tiny', '0.20';

on test => sub {
  requires 'Test::Deep';
  requires 'Test::More', '0.96';
};
